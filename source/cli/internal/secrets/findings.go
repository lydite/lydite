package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitdiff"
)

// report is gitleaks' JSON report: a bare array of leaks, one per match.
//
// It carries no errors key and no summary, so a run that failed writes the same
// empty array a clean one does. Whether gitleaks did the walk at all is read
// from its exit status against what this array holds, in ranToCompletion.
type report []leak

// leak mirrors the subset of one gitleaks entry lydite reads.
//
// Match and Secret are deliberately absent. Under --redact both read REDACTED,
// and the unredacted form of either is the credential — which is exactly what
// must not travel in scan.json or in a comment on a public pull request.
//
// StartColumn is a byte column and is one past the match's first byte: a match
// beginning its line reads 2, confirmed against the pinned gitleaks in
// testdata/gitleaks.json.
type leak struct {
	RuleID      string `json:"RuleID"`
	Description string `json:"Description"`
	File        string `json:"File"`
	StartLine   int    `json:"StartLine"`
	EndLine     int    `json:"EndLine"`
	StartColumn int    `json:"StartColumn"`
}

// carried reports whether a path is one git would carry out of the scan root.
//
// gitleaks walks every file under that root — a warm target/, an installed
// node_modules/, a dist/ — and offers no flag that scopes the walk: `dir`
// takes exactly one path and reads no .gitignore. So the claims are scoped
// instead of the walk. A file git will not carry cannot be committed by
// accident, which is the leak this gate exists to catch; a file that is
// untracked but not ignored is one `git add .` from being published, so it
// stays in scope, and so does everything under a nested repository git's
// answer stops at.
type carried func(path string) bool

// tracked asks git which paths it would carry out of dir: everything it
// tracks, plus everything untracked that .gitignore does not cover.
//
// gitdiff.Tracked is that one question, asked the way internal/orphan and the
// mutation worktree already ask it. A second ls-files here would agree with it
// only until one of the two learned something about git the other did not.
//
// Its paths are relative to dir and slash-separated, which is the shape a
// finding's Path carries, so the two compare as text.
//
// An empty answer is returned as errNoScope with a filter that keeps
// everything, because a filter that drops everything is a clean row over a
// tree nothing was scoped to.
func tracked(ctx context.Context, dir string) (carried, error) {
	paths, err := gitdiff.Tracked(ctx, dir)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return unscoped, errNoScope
	}
	nested, err := nestedRepositories(ctx, dir, paths)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return func(path string) bool {
		if set[path] {
			return true
		}
		return slices.ContainsFunc(nested, func(prefix string) bool {
			return strings.HasPrefix(path, prefix)
		})
	}, nil
}

// errNoScope reports that git named no path at all under the scan root.
//
// Distinct from a tree every one of whose leaks was filtered, and the
// distinction is the whole point: a scan root inside an ignored subtree — a
// vendored checkout, a --dir pointed at build output — is inside a work tree,
// lists nothing and exits zero, so every claim is dropped and the row reads as
// a scanned, clean tree. internal/orphan separates the same two answers with
// ErrNoFiles.
var errNoScope = errors.New("git lists no file it would carry under the scan root, so nothing gitleaks reported can be scoped to one")

// gitlinkMode is the index mode git records a submodule under.
const gitlinkMode = "160000"

// nestedRepositories is the prefixes beneath which git's answer for this
// repository says nothing at all, each with its trailing slash.
//
// `git ls-files` stops at a nested repository's boundary in both of its
// shapes: a submodule is one index entry naming the gitlink path itself, and
// an embedded repository — a directory holding a .git that no gitlink records
// — is listed as that directory, with a trailing slash, and not descended
// into. gitleaks walks both as ordinary source, so a leak underneath one is in
// the report and in neither list, and comparing the two as text drops it in
// silence.
//
// So a path under one of these prefixes is kept. Over-reporting is the
// direction a scope lydite could not establish already chooses, and the
// alternative is the failure this gate is least allowed to have.
func nestedRepositories(ctx context.Context, dir string, paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		if strings.HasSuffix(p, "/") {
			out = append(out, p)
		}
	}
	// --stage is the only form that carries an entry's mode, and the gitlink
	// mode is the only thing that tells a submodule's path from an ordinary
	// file's. -z for the reason gitdiff.Tracked passes it: a path git would
	// otherwise render as a quoted escape arrives intact.
	res := executil.RunQuiet(ctx, dir, "git", "ls-files", "-z", "--stage")
	if !res.Ok() {
		return nil, fmt.Errorf("git ls-files --stage: %w", res.Err)
	}
	for _, entry := range strings.Split(res.Output, "\x00") {
		// <mode> SP <object> SP <stage> TAB <path>
		mode, rest, ok := strings.Cut(entry, " ")
		if !ok || mode != gitlinkMode {
			continue
		}
		_, p, ok := strings.Cut(rest, "\t")
		if !ok || p == "" {
			continue
		}
		out = append(out, p+"/")
	}
	return out, nil
}

// unscoped holds every path, for a root git could not be asked about. The
// claims are what a scope lydite could not establish must not cost; the row
// says so instead, in result.
func unscoped(string) bool { return true }

// scanRoot places the paths gitleaks reports onto the scan root.
//
// gitleaks names a file the way its target named it: `lydite scan` passes ".",
// so the paths arrive relative and slash-separated, and an absolute target
// yields absolute paths instead. A path that is not reduced to the root's own
// shape matches neither git's answer nor the tree a site is read from, and
// the failure is silent — every claim filtered away and a clean row. The root
// is held with its symlinks resolved as well as literally, because darwin
// walks /var/folders/... as /private/var/folders/...
type scanRoot struct{ dir, resolved string }

func newScanRoot(dir string) scanRoot {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	return scanRoot{dir: abs, resolved: resolved}
}

// place is file as a path relative to the scan root, or "" for one that names
// nothing inside it.
//
// Empty rather than the path itself: a claim outside the root can be compared
// against neither git's answer nor the tree, so it is one lydite cannot place,
// and the caller reports it rather than dropping it.
func (r scanRoot) place(file string) string {
	if file == "" {
		return ""
	}
	if filepath.IsAbs(file) {
		return r.rel(file)
	}
	return inside(filepath.ToSlash(file))
}

// rel places an absolute path against either form of the root, resolving the
// path's own symlinks as a third candidate: the report and the root may each
// name the same file through a different link.
func (r scanRoot) rel(file string) string {
	files := []string{file}
	if resolved, err := filepath.EvalSymlinks(file); err == nil && resolved != file {
		files = append(files, resolved)
	}
	for _, base := range []string{r.dir, r.resolved} {
		for _, f := range files {
			rel, err := filepath.Rel(base, f)
			if err != nil {
				continue
			}
			if p := inside(filepath.ToSlash(rel)); p != "" {
				return p
			}
		}
	}
	return ""
}

// inside is a slash-separated path cleaned of "./" and ".." segments, or ""
// when what it names is the root itself or something above it.
func inside(p string) string {
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

// rotate is appended to every message, verbatim.
//
// gitleaks' own description ends at what it found, and an author who reads only
// that deletes the line, pushes, watches the row go green and has fixed
// nothing: the credential is in a pushed commit and stays valid until somebody
// revokes it. Message is the only text the author of a pull request reads, so
// the instruction goes there and not in Detail — a located claim becomes a
// review thread showing the message, and the reader who most needs this is the
// one who sees only that line.
const rotate = "Rotate this credential: deleting it does not undo the leak, because the commit that carries it is already pushed."

// separator joins a site's parts. It cannot occur in either, so a rule ending
// in one character and a prefix beginning with another cannot collide.
const separator = "\x1f"

// maxSiteRunes bounds one line's contribution to a claim's identity, as
// finding.Source bounds its own: a minified bundle is one line of several
// megabytes, and a match late in it would otherwise put the whole file into a
// Site that travels in the report document and is uploaded as an artifact.
//
// maxFileBytes bounds the read that produces it, for the same reason.
const (
	maxSiteRunes = 256
	maxFileBytes = 4 << 20
)

// findings is every leak in a file git would carry as a located claim, and
// everything the report named that could not become one.
//
// A leak lydite cannot place is returned rather than dropped: a parser that
// silently discards input is how a gate quietly stops working, and here the
// discarded thing is a credential somebody committed. A leak in a path keep
// excludes is a different thing and is dropped in silence — it is located
// perfectly well and is simply not a file this gate has a claim over.
func findings(dir string, rep report, keep carried) (out []finding.Finding, unplaced []string) {
	root := newScanRoot(dir)
	src := newTree(dir)
	cols := earliestColumnPerLine(root, rep)
	for _, l := range inSourceOrder(rep) {
		p := root.place(l.File)
		if p == "" || l.StartLine < 1 {
			// Neither a thread nor an identity can be made from this, so it is
			// returned rather than dropped and result fails the row on it: a
			// leak reported and never located is not a tree with no leak in it.
			unplaced = append(unplaced, fmt.Sprintf("gitleaks reported %s at %q line %d, which names no line inside the scan root", ruleOf(l), l.File, l.StartLine))
			continue
		}
		if !keep(p) {
			continue
		}
		end := 0
		if l.EndLine > l.StartLine {
			end = l.EndLine
		}
		// The cut is the earliest match on this line, not this claim's own:
		// gitleaks reports one leak per match, and a line with two matches
		// would otherwise put the first one's secret into the second one's
		// site, published in scan.json for text no rule flagged as its own.
		col := cols[lineKey{p, l.StartLine}]
		out = append(out, finding.Finding{
			Gate: Gate,
			// No Component. This gate is root-scoped, and an empty component is
			// what that already means; attributing a claim to whichever
			// component happens to contain its path is the ownership question
			// ADR 0033 refuses to answer.
			Path:    p,
			Line:    l.StartLine,
			EndLine: end,
			Rule:    l.RuleID,
			Message: message(l),
			// No Detail. Everything gitleaks would put there is either already a
			// field — the rule, the description — or is the secret.
			Site: site(l.RuleID, src.prefix(p, l.StartLine, col)),
		})
	}
	finding.Number(out)
	return out, unplaced
}

// lineKey names one line of one file, for grouping leaks that share it.
type lineKey struct {
	path string
	line int
}

// earliestColumnPerLine is, for every file and line a leak names, the
// smallest StartColumn reported for it.
//
// A line can carry more than one match — a chained .env export, a
// docker run with two -e flags, a DSN followed by a token — and gitleaks
// reports each as its own leak. Every claim on that line has to cut its site
// before the first of them, not just before itself, or the claim for the
// second match publishes the first match's secret as its own identity.
func earliestColumnPerLine(root scanRoot, rep report) map[lineKey]int {
	cols := make(map[lineKey]int, len(rep))
	for _, l := range rep {
		if l.StartLine < 1 {
			continue
		}
		p := root.place(l.File)
		if p == "" {
			continue
		}
		k := lineKey{p, l.StartLine}
		if cur, ok := cols[k]; !ok || l.StartColumn < cur { // [lydite:exclude_from_mutation][< vs <=
			// differ only when l.StartColumn == cur, and writing the same value back
			// changes nothing a caller can observe]
			cols[k] = l.StartColumn
		}
	}
	return cols
}

// inSourceOrder is the report's leaks by file and then by position.
//
// gitleaks scans files concurrently and its report's order varies between two
// runs over one tree — observed on the capture in testdata. Ordinals are
// counted in the order they are given, so two claims sharing a site would swap
// identities between runs, and the report document would list one run's claims
// in an order the next contradicts. Sorting is what makes "source order" true.
func inSourceOrder(rep report) []leak {
	sorted := slices.Clone(rep)
	slices.SortStableFunc(sorted, func(a, b leak) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if c := a.StartLine - b.StartLine; c != 0 {
			return c
		}
		return a.StartColumn - b.StartColumn
	})
	return sorted
}

// message is the rule's description with its trailing full stop normalised,
// then the instruction to rotate.
//
// A rule with no description still says which rule fired: a claim reading only
// "rotate this credential" names nothing an author can look up.
func message(l leak) string {
	desc := strings.TrimSpace(l.Description)
	if desc == "" {
		desc = "gitleaks matched " + ruleOf(l)
	}
	return strings.TrimRight(desc, ".") + ". " + rotate
}

// ruleOf is the rule that fired, or a word for a report entry that named none.
func ruleOf(l leak) string {
	if l.RuleID == "" {
		return "an unnamed rule"
	}
	return l.RuleID
}

// site identifies a claim independently of where it sits in the file.
//
// It is the rule with the start line's text *up to* the match, and this is the
// one scanner whose site is not the text it fired on: that text is the
// credential, and a site travels in scan.json and into the standing comment on
// a public pull request. A gate that publishes the secret it found in order to
// identify it stably is worse than no gate.
//
// The cost is named in ADR 0035 and accepted: two secrets under one rule with
// no distinguishing prefix in one file are ordinals 0 and 1, so rotating the
// first re-identifies the second.
func site(rule, prefix string) string { return rule + separator + prefix }

// tree reads the working-tree lines a site is cut from, each file once.
//
// finding.Source is what every other gate reads a site through and cannot serve
// this one: it answers with a line already normalised, and normalising moves
// every byte column in it, while a site here is the line cut at the column
// gitleaks reported. So the read is local and the normalisation is not —
// finding.Normalise is what the cut goes through, so a reindented line
// re-identifies nothing here either.
//
// Reads are confined to the root rather than checked against it, for the reason
// finding.Source confines its own: a path reaching here was reported by a tool
// reading a repository lydite does not own, and the text it names becomes a
// Site that lydite publishes.
type tree struct {
	root  *os.Root
	files map[string][]string
}

// newTree reads paths relative to root, which is the root every finding's Path
// is already relative to.
//
// A root that cannot be opened yields a tree that answers empty for everything:
// identity is what is lost, and the gate has already reported what it found.
func newTree(root string) *tree {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return &tree{files: map[string][]string{}}
	}
	return &tree{root: opened, files: map[string][]string{}}
}

// prefix is the file's nth line up to col, normalised and bounded.
//
// Empty for a line it cannot read — a file changed under the run, a path
// outside the tree, a line past the end. Empty is the right answer rather than
// an error: the caller is building an identity, and a missing ingredient makes
// two claims in one file share one, which the ordinal then separates.
func (t *tree) prefix(path string, n, col int) string {
	if n < 1 {
		return ""
	}
	lines, ok := t.files[path]
	if !ok {
		lines = t.read(path)
		t.files[path] = lines
	}
	if n > len(lines) {
		return ""
	}
	line := lines[n-1]
	cut := clip(finding.Normalise(line[:prefixEnd(line, col)]))
	if !safePrefix.MatchString(cut) {
		return ""
	}
	return cut
}

// safePrefix matches a prefix holding nothing but one identifier-shaped
// token, optionally followed by a single opening bracket — the only shape
// of "assignment context" this package is willing to publish. It anchors
// both ends, so it accepts the whole cut or none of it.
//
// Cutting at the earliest match gitleaks flagged on a line only protects
// flagged matches from each other. A line can also carry a credential no
// rule flagged — export DB_PASSWORD=hunter2 API_TOKEN=<token>, a JSON
// {"password": "hunter2!", "api_key": …}, a DSN's password ahead of an
// api_key parameter, mysql -u root -phunter2 --api-key=<token> — and each
// is exactly as much a secret as the one gitleaks found. A blocklist of
// dangerous characters keeps discovering a new command shape it forgot: a
// flag needs no `=`, no quote, no colon. So this is a denylist of nothing
// and an allowlist of one shape instead — refusing every prefix that is
// not provably just an identifier, rather than guessing which punctuation
// is safe. Checked after clipping, so a clipped-away tail cannot cause an
// already-safe head to be discarded too.
var safePrefix = regexp.MustCompile(`^(\p{L}[\p{L}0-9_.]*)?[(\[]?$`)

func (t *tree) read(path string) []string {
	if t.root == nil {
		return nil
	}
	f, err := t.root.Open(filepath.FromSlash(path))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	// One byte past the cap, so a file exactly at it is still read whole and one
	// over it is refused rather than truncated mid-line — a truncated last line
	// is a Site identifying a claim by text the file does not hold.
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// prefixEnd is the byte index the site's prefix ends at.
//
// gitleaks reports a start column one byte past the match's first byte — a
// match beginning its line reads 2, confirmed against the pinned version in
// testdata/gitleaks.json — so the cut is two before it. Being before the match
// is the whole of what keeps the secret out of the site, and a column that
// stops meaning this shortens the prefix rather than lengthening it.
//
// Clamped into the line, and back to a rune boundary: a cut past the end would
// panic, and one inside a rune would put half of a character into a fingerprint
// ingredient, where it renders as a replacement character in every document
// that carries the claim.
func prefixEnd(line string, col int) int {
	end := min(max(col-2, 0), len(line))
	for end > 0 && end < len(line) && !utf8.RuneStart(line[end]) {
		end--
	}
	return end
}

// clip bounds a prefix's contribution to a claim's identity.
//
// Stated as a clamp rather than as a comparison, so there is no boundary to be
// wrong about: a prefix at exactly the cap and one under it take the same path.
func clip(s string) string {
	runes := []rune(s)
	return string(runes[:min(len(runes), maxSiteRunes)])
}

// readReport reads the report gitleaks was told to write.
//
// Absent and unparseable are one thing to the caller: a run whose claims lydite
// does not have. Treating either as an empty report is what would pass a
// leaking tree on the strength of a parse error.
func readReport(path string) (report, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is our own CreateTemp result, not user input
	if err != nil {
		return nil, err
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("parsing gitleaks' report: %w", err)
	}
	return rep, nil
}

// leaksExit is the status gitleaks exits with when it found a leak.
//
// It is the default of gitleaks' own --exit-code flag, which argv deliberately
// does not pass. Any other non-zero status is gitleaks failing to be a scanner
// rather than reporting as one — 126 for a flag it does not know, and whatever
// a signal leaves behind.
const leaksExit = 1

// ranToCompletion reports whether gitleaks' exit is accounted for by the report
// it wrote.
//
// A clean exit is, and so is leaksExit beside a report naming at least one
// leak. Nothing else is: gitleaks exits 1 for a directory it could not walk as
// well as for a leak, and an exit carrying no status at all is a binary that
// never started or a context that was cancelled. Every one of those leaves an
// empty report, which reads as a scanned, clean tree to everything downstream
// unless the status is checked against it here.
//
// A walk that failed after finding something exits 1 with a non-empty report
// and is indistinguishable from one that found it and finished. What it costs
// is a run whose surviving claims were all filtered away passing on a partial
// walk; the report gitleaks writes carries nothing that would separate them.
func ranToCompletion(exit error, rep report) bool {
	if exit == nil {
		return true
	}
	var status *exec.ExitError
	return errors.As(exit, &status) && status.ExitCode() == leaksExit && len(rep) > 0
}

// verdict is the row's outcome: nil for a tree this gate scanned and found
// nothing in, and an error naming which of the other things happened.
//
// Ordered by what the reader of a red row can act on. A gate with no claims to
// decide from — an unreadable report, a gitleaks that did not finish its walk —
// is said ahead of anything derived from claims lydite does not have; an
// unscoped run ahead of the claims it over-reports; and gitleaks' own exit is
// kept verbatim for the one case where it is the whole story.
func verdict(exit error, readErr, scopeErr error, rep report, unplaced []string, claims int) error {
	switch {
	case readErr != nil:
		return fmt.Errorf("the secret gate has no report to decide on: %w", readErr)
	case !ranToCompletion(exit, rep):
		return fmt.Errorf("gitleaks did not finish its walk, so nothing here is a statement about the tree: %w", exit)
	case scopeErr != nil:
		return fmt.Errorf("the secret gate could not be scoped to the files git would carry: %w", scopeErr)
	case len(unplaced) > 0:
		return errors.New("gitleaks reported a leak lydite could not locate")
	case claims > 0:
		if exit == nil {
			// A leak in the report beside a clean exit is gitleaks
			// contradicting itself, and the leak is the half that must survive.
			return errors.New("gitleaks reported a leak")
		}
		return exit
	}
	return nil
}

// scopeFailure is the scope error this report has to be decided under.
//
// Every reason git could not be asked is one, whatever the report holds. An
// empty scope is one only against a report that names a leak: git listing
// nothing and gitleaks finding nothing agree with each other, and failing on
// that pair would fail every scan of a tree with no file in it — a fresh
// repository, a component directory holding only ignored output. The moment
// the report names a leak the two disagree, and the filter that would silently
// drop every one of them is the gate reporting clean over a tree it never
// scoped.
func scopeFailure(err error, rep report) error {
	if errors.Is(err, errNoScope) && len(rep) == 0 {
		return nil
	}
	return err
}

// result is everything decided after gitleaks has exited, given its result and
// the file it was told to write.
//
// Split from the invocation so it can be tested against a report on disk: a
// test that had to run gitleaks to reach these decisions would be testing the
// machine's gitleaks.
//
// keep and scopeErr are what tracked answered: the paths git would carry, or
// the reason it could not say. A scope lydite could not establish costs the
// row rather than the claims — every leak is reported and the row fails saying
// why, because a pass there is indistinguishable from a tree that was scoped
// and clean. Which reasons count as one is scopeFailure's, because an empty
// answer from git is only a failure against a report that names something.
//
// The row follows the claims that survived that filter and not gitleaks' exit
// status, which fails for a leak anywhere it walked including the ignored
// output no claim here survives. What the status is still read for is whether
// gitleaks walked at all, because a run that did not is the one thing an empty
// claim list cannot tell apart from a clean tree.
//
// Three outcomes are this gate failing rather than this gate's finding, and
// each fails the row with its reason in Detail: a report lydite could not read,
// a leak it could not place, and a scope git could not be asked for. A failing
// row is the strictest thing available here and not the precise one — the
// grammar's own status for a gate that could not run is the amber
// StatusUnmeasured, and executil.Result carries a verdict as an error or
// nothing, so resultRows has only pass and fail to render these as.
func result(r executil.Result, dir, reportPath string, keep carried, scopeErr error) executil.Result {
	// Detail because this is lydite's own statement rather than gitleaks', and
	// report() prints Detail under a failing row and nothing else.
	var notes []string
	rep, readErr := readReport(reportPath)
	scopeErr = scopeFailure(scopeErr, rep)
	if scopeErr != nil {
		keep = unscoped
		notes = append(notes, "these claims are not scoped to the files git would carry: "+scopeErr.Error())
	}
	var unplaced []string
	if readErr == nil {
		r.Findings, unplaced = findings(dir, rep, keep)
		// Said whatever the verdict, because a leak lydite could not place is
		// lost from the document either way.
		notes = append(notes, unplaced...)
	} else {
		notes = append(notes, "lydite could not read the report gitleaks was told to write, so this run states nothing about the tree: "+readErr.Error())
	}
	if !ranToCompletion(r.Err, rep) {
		notes = append(notes, "gitleaks exited on something other than the leaks it reported, so the tree may not have been walked whole: "+r.Err.Error())
	}
	r.Err = verdict(r.Err, readErr, scopeErr, rep, unplaced, len(r.Findings))
	if len(notes) > 0 {
		r.Detail = strings.Join(notes, "\n")
	}
	return r
}
