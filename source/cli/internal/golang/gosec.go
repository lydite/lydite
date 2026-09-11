package golang

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// gosecReport mirrors the subset of `gosec -fmt json` lydite reads.
//
// Three things about this shape were confirmed against the pinned gosec
// directly rather than from its documentation, because each is easy to guess
// wrong: `file` is an absolute path even when gosec was given `./...`, `line`
// is a *string* and may be a range ("10-12"), and the build-error key is
// literally "Golang errors" — a space and a capital, which no struct tag
// convention would produce.
type gosecReport struct {
	// BuildErrors is keyed by file, with one entry under "" carrying the
	// package-level message. A package that did not compile is a package
	// gosec scanned nothing in.
	BuildErrors map[string][]gosecBuildError `json:"Golang errors"`
	Issues      []gosecIssue                 `json:"Issues"`
}

type gosecBuildError struct {
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Errorf string `json:"error"`
}

type gosecIssue struct {
	Severity string `json:"severity"`
	RuleID   string `json:"rule_id"`
	Details  string `json:"details"`
	File     string `json:"file"`
	// Code is the source excerpt gosec quotes, already line-numbered.
	Code string `json:"code"`
	// Line is one line ("11") or a range ("10-12"), as text.
	Line string `json:"line"`
}

// gosecSpan is the first and last line a `line` field names.
//
// A range collapses to its own start when the end is missing or smaller, so a
// malformed field costs the claim its extent and never its location. Returning
// zero for an unparseable start is what makes the claim unanchorable rather
// than anchored to line zero of a file.
func gosecSpan(s string) (start, end int) {
	first, rest, ranged := strings.Cut(s, "-")
	start, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0, 0
	}
	if !ranged {
		return start, 0
	}
	end, err = strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || end <= start {
		return start, 0
	}
	return start, end
}

// gosecFindings is every issue in the report as a located claim.
//
// The site is the rule with the text it fired on, read from the tree through
// finding.Source rather than from the report's own `code` excerpt. The excerpt
// spans three lines and carries gosec's own line-number prefixes, so it
// identifies a neighbourhood rather than a line and changes whenever a
// neighbour does; Source answers with the one line, normalised and bounded by
// the same rule every other gate's site obeys.
//
// The excerpt is what becomes the finding's Detail, which is the reader's half
// of the same question.
func gosecFindings(dir string, report gosecReport) []finding.Finding {
	src := finding.NewSource(dir)
	// Resolved once, beside the Source that is already hoisted out of the
	// loop: the directory does not vary across a report, and re-resolving it
	// per issue means a stat for every finding gosec made.
	root := resolvedRoot(dir)
	var out []finding.Finding
	for _, issue := range report.Issues {
		path := root.rel(issue.File)
		start, end := gosecSpan(issue.Line)
		if path == "" || start < 1 {
			// A claim carrying line zero of the component's own directory is
			// not one anything can anchor or tell from the next. The row still
			// fails on gosec's exit status, which is what reports it.
			continue
		}
		out = append(out, finding.Finding{
			Gate:     "gosec",
			Path:     path,
			Line:     start,
			EndLine:  end,
			Rule:     issue.RuleID,
			Severity: strings.ToLower(issue.Severity),
			Message:  issue.Details,
			Detail:   excerpt(issue.Code),
			Site:     issue.RuleID + "\x1f" + src.Line(path, start),
		})
	}
	finding.Number(out)
	return out
}

// excerpt is a tool's quoted source block as the lines a reader sees.
//
// The trailing newline every such block ends with would otherwise become an
// empty final line, which renders as a blank row under every finding.
func excerpt(code string) []string {
	code = strings.TrimRight(code, "\n")
	if code == "" {
		return nil
	}
	return strings.Split(code, "\n")
}

// resolvedRoot is a component directory in the forms a path may have to be
// rebased against, worked out once.
//
// `lydite scan --dir .` builds a component directory that is relative while
// gosec always reports an absolute path, and Rel refuses a pair that is not
// both one or both the other. macOS makes /tmp a link to /private/tmp, so a
// root given as one and reported as the other shares no prefix at all — a real
// configuration rather than a broken one, and the reason the resolved form is
// kept as well.
type resolvedDir struct{ abs, real string }

func resolvedRoot(dir string) resolvedDir {
	r := resolvedDir{abs: dir}
	if abs, err := filepath.Abs(dir); err == nil {
		r.abs = abs
	}
	if real, err := filepath.EvalSymlinks(r.abs); err == nil {
		r.real = real
	}
	return r
}

// rel is file named from this directory.
//
// The fallback is the path unchanged rather than an error, because a path that
// will not rebase is still a true statement about a file — it is only a worse
// one. What it must not do is silently produce a path climbing out of the
// component with `..`, which names one file under two identities depending on
// which component's scan reached it.
func (d resolvedDir) rel(file string) string {
	if file == "" {
		return ""
	}
	if rel, err := filepath.Rel(d.abs, file); err == nil && !escapes(rel) {
		return filepath.ToSlash(rel)
	}
	if d.real != "" {
		if real, err := filepath.EvalSymlinks(file); err == nil {
			if rel, err := filepath.Rel(d.real, real); err == nil && !escapes(rel) {
				return filepath.ToSlash(rel)
			}
		}
	}
	return filepath.ToSlash(file)
}

// relTo is path named from dir, for a caller with one path to rebase.
//
// The fallback is the path unchanged rather than an error, because a path that
// will not rebase is still a true statement about a file — it is only a worse
// one. What it must not do is silently produce a path climbing out of the
// component with `..`, which names one file under two identities depending on
// which component's scan reached it.
//
// Symlinks are resolved only on the failing branch. macOS makes /tmp a link to
// /private/tmp, so a scan root given as one and reported as the other shares
// no prefix at all, and that is a real configuration rather than a broken one.
func relTo(dir, file string) string { return resolvedRoot(dir).rel(file) }

// escapes reports whether a rebased path climbs out of the directory it was
// rebased against.
//
// The test is the `..` element itself and not the prefix `..`, which a file
// named `..rc` in the directory also carries. Such a file is not reachable
// through `gosec ./...` today, because the go command ignores a path element
// beginning with a dot — so this is a security-shaped check said precisely
// rather than a live defect. A check of this kind that is right by accident is
// one nobody can reason about the next time its caller changes.
func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// buildErrors is every compile failure the report names, each said once.
//
// Paths are named from dir, so a build failure and a finding in the same
// report agree about where a file is.
//
// The report states each failure twice: once keyed by the file it is in, and
// once under the empty key as the whole of what the compiler printed for the
// package. The file-keyed entries are the same failures with a location
// attached, so they are what a reader gets; the aggregate is the fallback for
// a failure the compiler attributed to no file at all.
func buildErrors(dir string, report gosecReport) []string {
	// Resolved once, as the findings loop beside it does: the directory does
	// not vary across a report.
	root := resolvedRoot(dir)
	var located, aggregate []string
	for _, file := range sortedKeys(report.BuildErrors) {
		for _, e := range report.BuildErrors[file] {
			msg := strings.TrimSpace(e.Errorf)
			if msg == "" {
				continue
			}
			if file == "" {
				aggregate = append(aggregate, msg)
				continue
			}
			// Rebased, for the reason every issue in the same report is: the
			// key is the absolute path gosec scanned at, and this text reaches
			// Result.Detail, which the standing comment quotes. A local
			// `lydite publish` would otherwise put the developer's own
			// checkout path into a published comment.
			located = append(located, fmt.Sprintf("%s:%d: %s", root.rel(file), e.Line, msg))
		}
	}
	// Every located failure, plus each aggregate that names one nothing
	// located covers. The aggregate is the whole of what the compiler printed
	// for a package, so it repeats the failures already listed with a file
	// beside them — but a package whose failure the compiler attributed to no
	// file has only this, and dropping every aggregate would leave that
	// package unexplained while the row still failed for it.
	for _, whole := range aggregate {
		if !covered(whole, located) {
			located = append(located, whole)
		}
	}
	return located
}

// covered reports whether an aggregate says only what the located entries
// already say.
func covered(whole string, located []string) bool {
	for _, line := range strings.Split(whole, "\n") {
		line = strings.TrimSpace(line)
		// The `# package/path` header names the package rather than a
		// failure, so it is not what decides this.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		found := false
		for _, one := range located {
			if strings.Contains(one, line) || strings.Contains(line, messageOf(one)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// messageOf is a located entry without the `file:line: ` this package put in
// front of it, which is what the compiler's own line is compared against.
func messageOf(located string) string {
	if _, rest, ok := strings.Cut(located, ": "); ok {
		return rest
	}
	return located
}

// sortedKeys is a map's keys in a stable order, so a run reports its build
// failures in the same order as the last one.
func sortedKeys[V any](m map[string][]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// parseGosec reads a report, saying whether it could.
//
// A report that will not parse leaves the run entirely to gosec's own exit
// status and output. Inventing a verdict from an unreadable report is the one
// thing worse than having no findings: it would either fail a clean run or
// pass a dirty one on the strength of a parse error.
func parseGosec(data []byte) (gosecReport, bool) {
	var report gosecReport
	if err := json.Unmarshal(data, &report); err != nil {
		return gosecReport{}, false
	}
	return report, true
}

// gosecArgv is the one invocation, as argv.
//
// `-stdout -verbose text` beside `-fmt json -out <file>` is what makes the
// report a copy rather than a replacement: without it the terminal and the CI
// log see nothing, and this check is one whose findings are the point. That
// pairing is the decision here and the thing a regression would silently undo.
//
// -exclude-generated skips files carrying the standard
// "Code generated ... DO NOT EDIT." header. Findings there are not actionable:
// the only fix is to change the generator or its input, and a `#nosec`
// annotation would be erased by the next regeneration. It matches how
// generated code is treated elsewhere in the pipeline — golangci-lint's
// `exclusions: generated` and semgrep's own generated-file skip.
func gosecArgv(outPath string) []string {
	return []string{
		"-exclude-generated",
		"-fmt", "json", "-out", outPath,
		"-stdout", "-verbose", "text",
		"./...",
	}
}

// runGosec runs gosec and reads a JSON copy of its report.
//
// The tool keeps printing its own findings: -stdout -verbose text is what
// makes the JSON a copy rather than a replacement, so the terminal and the CI
// log see exactly what they saw before and Result.Detail stays empty. Only
// Result.Findings is new.
// [lydite:exclude_from_coverage][the self-scan runs gosec over source/cli on
// every run; a unit test here would run the machine's own gosec rather than
// lydite's invocation, which gosecArgv states and TestGosecArgvKeepsTheReportACopy
// asserts]
func runGosec(ctx context.Context, dir string, env []string, bin string) executil.Result {
	out, err := os.CreateTemp("", "lydite-gosec-*.json")
	if err != nil {
		// Detail as well as Err, for the reason an install failure carries
		// one: report() prints Detail under a failing row and nothing else.
		return executil.Result{Name: "gosec", Err: err, Detail: err.Error()}
	}
	outPath := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(outPath) }()

	r := executil.RunEnv(ctx, dir, env, bin, gosecArgv(outPath)...)
	r.Name = "gosec"

	return gosecResult(r, dir, outPath)
}

// gosecResult is everything decided after gosec has exited, given its result
// and the file it was told to write.
//
// Split from the invocation so it can be tested against a report on disk.
// Whether a claim is made at all, and whether a package that did not compile
// reads as a pass, are the decisions worth pinning — and a test that had to
// run gosec to reach them would be testing the machine's gosec.
func gosecResult(r executil.Result, dir, outPath string) executil.Result {
	data, readErr := os.ReadFile(outPath) // #nosec G304 -- outPath is our own CreateTemp result, not user input
	if readErr != nil {
		// No report to read: leave gosec's own exit status and output as-is
		// rather than inventing a verdict.
		return r
	}
	report, ok := parseGosec(data)
	if !ok {
		return r
	}
	r.Findings = gosecFindings(dir, report)
	// A package that did not compile is a package gosec scanned nothing in,
	// and it reports that in the report rather than in its exit status. The
	// status happens to be non-zero today, so this changes no verdict lydite
	// currently reaches; it is here because a clean exit beside an empty
	// scan is the failure this branch exists to refuse, and nothing about
	// gosec's contract promises the status will keep covering it.
	if errs := buildErrors(dir, report); len(errs) > 0 && r.Ok() {
		// Detail because this is lydite's verdict rather than gosec's, and
		// report() prints Detail under a failing row and nothing else. It is
		// not findings — those stay out of Detail, which is what keeps a
		// tool that prints its own from being reprinted under it.
		r.Err = errors.New("gosec scanned nothing in at least one package: it did not compile")
		r.Detail = strings.Join(errs, "\n")
	}
	return r
}
