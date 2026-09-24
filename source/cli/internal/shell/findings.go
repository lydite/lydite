package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// report mirrors the subset of ShellCheck's json1 output lydite reads.
type report struct {
	Comments []comment `json:"comments"`
}

type comment struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
	// Column is one-based and counts characters, a tab included as one — the
	// json1 format's guarantee, and what a site's cut is made at.
	Column  int    `json:"column"`
	Level   string `json:"level"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// separator joins a site's parts. It cannot occur in either, so a rule ending
// in one character and a prefix beginning with another cannot collide.
const separator = "\x1f"

// maxSiteRunes bounds one line's contribution to a claim's identity, as
// finding.Source bounds its own: a line of several megabytes would otherwise
// put the whole of it into a Site that travels in the report document.
//
// maxFileBytes bounds the read that produces it, for the same reason.
const (
	maxSiteRunes = 256
	maxFileBytes = 4 << 20
)

// rule is ShellCheck's identifier for a diagnostic, in the form its own
// directives and wiki name it: `SC2086`.
func rule(code int) string { return "SC" + strconv.Itoa(code) }

// wiki is where ShellCheck documents a rule — the one thing a reader of the
// claim needs beyond the message itself.
func wiki(code int) string { return "https://www.shellcheck.net/wiki/" + rule(code) }

// findings is every comment as a located claim, in source order.
//
// The site is the rule with the start line's text up to the earliest column
// any comment on that line reports, not up to this comment's own. A line can
// carry more than one diagnostic — two unquoted expansions on one `echo`, a
// quoting warning after an assignment — and a cut at each comment's own column
// puts the text of every earlier match into the later one's site, published in
// scan.json for text no rule there flagged as its own.
func findings(dir string, rep report) []finding.Finding {
	src := newTree(dir)
	cols := earliestColumnPerLine(rep)
	var out []finding.Finding
	for _, c := range inSourceOrder(rep) {
		p := place(c.File)
		if p == "" || c.Line < 1 {
			continue
		}
		end := 0
		if c.EndLine > c.Line {
			end = c.EndLine
		}
		out = append(out, finding.Finding{
			Gate:     Gate,
			Path:     p,
			Line:     c.Line,
			EndLine:  end,
			Rule:     rule(c.Code),
			Severity: strings.ToLower(c.Level),
			Message:  strings.TrimSpace(c.Message),
			Detail:   []string{wiki(c.Code)},
			Site:     rule(c.Code) + separator + src.prefix(p, c.Line, cols[lineKey{p, c.Line}]),
		})
	}
	finding.Number(out)
	return out
}

// place is a reported file as a path relative to the directory ShellCheck ran
// in: argv hands each script over as `./<path>`, and ShellCheck names it back
// the way it was given.
func place(file string) string {
	return strings.TrimPrefix(filepath.ToSlash(file), "./")
}

// inSourceOrder is the report's comments by file and then by position.
//
// ShellCheck does not report files in the order it was handed them — a
// captured run given `./scripts/a.sh ./b.bash` reports b.bash first — and
// ordinals are counted in the order they are given, so two claims sharing a
// site would swap identities between two runs that ordered them differently.
func inSourceOrder(rep report) []comment {
	sorted := slices.Clone(rep.Comments)
	slices.SortStableFunc(sorted, func(a, b comment) int {
		if c := strings.Compare(place(a.File), place(b.File)); c != 0 {
			return c
		}
		if c := a.Line - b.Line; c != 0 {
			return c
		}
		return a.Column - b.Column
	})
	return sorted
}

// lineKey names one line of one file, for grouping the comments that share it.
type lineKey struct {
	path string
	line int
}

// earliestColumnPerLine is, for every file and line a comment names, the
// smallest column reported for it.
func earliestColumnPerLine(rep report) map[lineKey]int {
	cols := make(map[lineKey]int, len(rep.Comments))
	for _, c := range rep.Comments {
		p := place(c.File)
		if p == "" || c.Line < 1 {
			continue
		}
		k := lineKey{p, c.Line}
		if cur, ok := cols[k]; ok {
			cols[k] = min(cur, c.Column)
		} else {
			cols[k] = c.Column
		}
	}
	return cols
}

// findingsDetail is the claims a run made, as the text report() prints under
// its failing row.
//
// ShellCheck runs once, in JSON mode, so nothing rendered for a human reaches
// the terminal on its own and this is the only place a reader is told what was
// found.
func findingsDetail(findings []finding.Finding) string {
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "%s:%d  %s  %s  %s\n", f.Path, f.Line, f.Rule, f.Severity, f.Message)
		for _, line := range f.Detail {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	return b.String()
}

// result is one run read as claims, with the Detail a failing row needs
// rendered from them.
//
// ShellCheck's exit status stays the verdict — 1 for a diagnostic, 2 for a file
// it could not read, 3 and 4 for an invocation it refused — and a finding count
// never becomes one. What its status cannot be trusted with is a clean exit
// beside a report that does not parse: that is a run lydite cannot show checked
// anything, and it fails rather than passing on the status alone.
func result(dir string, r executil.Result) executil.Result {
	var rep report
	if err := json.Unmarshal([]byte(r.Output), &rep); err != nil {
		if r.Ok() {
			r.Err = fmt.Errorf("shellcheck exited cleanly and its JSON report did not parse: %w", err)
		}
		r.Detail = detailOf(r, "")
		return r
	}
	r.Findings = findings(dir, rep)
	if r.Ok() {
		return r
	}
	r.Detail = detailOf(r, findingsDetail(r.Findings))
	return r
}

// detailOf is what a failing row prints: the claims it located, then whatever
// ShellCheck said on stderr about what it could not do — a file it could not
// open locates nothing and is reported only there. A failure that said neither
// states its status outright, so a row never fails with nothing under it.
func detailOf(r executil.Result, claims string) string {
	parts := []string{}
	if claims != "" {
		parts = append(parts, strings.TrimRight(claims, "\n"))
	}
	if msg := strings.TrimSpace(r.Stderr); msg != "" {
		parts = append(parts, msg)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s failed (%v) and its JSON report names no diagnostic.", Gate, r.Err)
	}
	return strings.Join(parts, "\n")
}

// tree reads the working-tree lines a site is cut from, each file once.
//
// finding.Source is what most gates read a site through and cannot serve this
// one: it answers with a line already normalised, and normalising moves every
// column in it, while a site here is the line cut at the column ShellCheck
// reported. So the read is local and the normalisation is not — the cut goes
// through finding.Normalise, so a reindented line re-identifies nothing.
//
// Reads are confined to the root rather than checked against it, for the
// reason finding.Source confines its own: the text a path names becomes a Site
// that lydite publishes.
type tree struct {
	root  *os.Root
	files map[string][]string
}

// newTree reads paths relative to root, which is the root every finding's Path
// is relative to.
//
// A root that cannot be opened yields a tree that answers empty for
// everything: identity is what is lost, and the gate has already reported what
// it found.
func newTree(root string) *tree {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return &tree{files: map[string][]string{}}
	}
	return &tree{root: opened, files: map[string][]string{}}
}

// prefix is the file's nth line before column col, normalised and bounded.
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
	runes := []rune(strings.TrimRight(lines[n-1], "\r"))
	// Columns are one-based characters, so the text before column col is its
	// first col-1 runes, clamped into the line.
	cut := finding.Normalise(string(runes[:min(max(col-1, 0), len(runes))]))
	clipped := []rune(cut)
	return string(clipped[:min(len(clipped), maxSiteRunes)])
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
	// One byte past the cap, so a file exactly at it is still read whole and
	// one over it is refused rather than truncated mid-line — a truncated last
	// line is a Site identifying a claim by text the file does not hold.
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return nil
	}
	return strings.Split(string(data), "\n")
}
