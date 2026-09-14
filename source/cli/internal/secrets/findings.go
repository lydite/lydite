package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// report is gitleaks' JSON report: a bare array of leaks, one per match.
//
// It carries no errors key and no summary, which is why the exit status decides
// the row here and there is no Semgrep-style exception for a tool that
// under-reports its own failure.
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

// findings is every leak as a located claim, and everything the report named
// that could not become one.
//
// A leak lydite cannot place is returned rather than dropped: a parser that
// silently discards input is how a gate quietly stops working, and here the
// discarded thing is a credential somebody committed.
func findings(dir string, rep report) (out []finding.Finding, unplaced []string) {
	src := newTree(dir)
	cols := earliestColumnPerLine(rep)
	for _, l := range inSourceOrder(rep) {
		path := filepath.ToSlash(strings.TrimPrefix(l.File, "./"))
		if path == "" || l.StartLine < 1 {
			// Neither a thread nor an identity can be made from this, and the
			// row still fails on gitleaks' own exit status. What must not happen
			// is that it goes unsaid.
			unplaced = append(unplaced, fmt.Sprintf("gitleaks reported %s at %q line %d, which names no file line", ruleOf(l), l.File, l.StartLine))
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
		col := cols[lineKey{path, l.StartLine}]
		out = append(out, finding.Finding{
			Gate: Gate,
			// No Component. This gate is root-scoped, and an empty component is
			// what that already means; attributing a claim to whichever
			// component happens to contain its path is the ownership question
			// ADR 0033 refuses to answer.
			Path:    path,
			Line:    l.StartLine,
			EndLine: end,
			Rule:    l.RuleID,
			Message: message(l),
			// No Detail. Everything gitleaks would put there is either already a
			// field — the rule, the description — or is the secret.
			Site: site(l.RuleID, src.prefix(path, l.StartLine, col)),
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
func earliestColumnPerLine(rep report) map[lineKey]int {
	cols := make(map[lineKey]int, len(rep))
	for _, l := range rep {
		if l.StartLine < 1 {
			continue
		}
		path := filepath.ToSlash(strings.TrimPrefix(l.File, "./"))
		if path == "" {
			continue
		}
		k := lineKey{path, l.StartLine}
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
	return clip(finding.Normalise(line[:prefixEnd(line, col)]))
}

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

// parseReport reads a report, saying whether it could.
//
// A report that will not parse leaves the verdict entirely to gitleaks' own
// exit status and output. Inventing one from an unreadable report is the one
// thing worse than having no findings: it would either fail a clean run or pass
// a leaking one on the strength of a parse error.
func parseReport(data []byte) (report, bool) {
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, false
	}
	return rep, true
}

// result is everything decided after gitleaks has exited, given its result and
// the file it was told to write.
//
// Split from the invocation so it can be tested against a report on disk: a
// test that had to run gitleaks to reach these decisions would be testing the
// machine's gitleaks.
//
// The exit status decides the row. A missing or unparseable report costs the
// run its findings and changes no verdict, which is the fallback ADR 0035
// states — the report has no error array, so there is nothing in it that could
// contradict the status.
func result(r executil.Result, dir, reportPath string) executil.Result {
	data, readErr := os.ReadFile(reportPath) // #nosec G304 -- reportPath is our own CreateTemp result, not user input
	if readErr != nil {
		return r
	}
	rep, ok := parseReport(data)
	if !ok {
		return r
	}
	found, unplaced := findings(dir, rep)
	r.Findings = found
	if len(unplaced) > 0 {
		// Detail because this is lydite's own statement rather than gitleaks',
		// and report() prints Detail under a failing row and nothing else. It is
		// said whatever the status, because a leak lydite could not place is
		// lost from the document either way.
		r.Detail = strings.Join(unplaced, "\n")
		if r.Ok() {
			// A leak reported beside a clean exit is a pass nothing else would
			// contradict.
			r.Err = errors.New("gitleaks reported a leak lydite could not locate")
		}
	}
	return r
}
