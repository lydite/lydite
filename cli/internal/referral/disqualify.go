package referral

import (
	"fmt"
	"path"
	"strings"

	"lydite/lydite/internal/config"
)

// Disqualification is one reason a change may not take the unattended path,
// with the evidence that produced it.
//
// One is reported per kind per file, never per occurrence. A file that adds
// forty suppressions has one problem, not forty, and forty rows saying so
// would bury the referral line underneath them.
type Disqualification struct {
	// Kind is the short name of the veto, for the report's label.
	Kind string
	// Path is the file that tripped it, and with Kind forms the identity of
	// the finding.
	Path string
	// Evidence names what was found. A veto the reader cannot locate is one
	// they cannot argue with.
	Evidence string
}

// suppressionTokens are annotations that turn a finding off.
//
// Every one of them is a way of satisfying a check by doing less checking,
// which is the dividing line the whole referral model rests on: these are not
// measurements of the code, they are evidence that something tried to make a
// verdict go away, and they must never be clearable by the thing that
// produced them.
// The whole-file and whole-crate forms sit alongside the per-line ones
// because they are strictly more powerful: `#![allow(clippy::all)]` silences
// for an entire crate the very check internal/rust runs, and `@ts-nocheck`
// turns off type checking for a whole file. Catching only the narrow form
// would veto the small suppression and wave the large one through.
var suppressionTokens = []string{
	"nosemgrep",
	"#nosec",
	"//nolint",
	"#[allow(",
	"#![allow(",
	"#[expect(",
	"#![expect(",
	"biome-ignore",
	"eslint-disable",
	"@ts-ignore",
	"@ts-expect-error",
	"@ts-nocheck",
}

// skipTokens stop a test from running or from being counted.
//
// The `.only` forms belong here even though they read as a focusing tool
// rather than a skip: `describe.only` silently stops every *other* test in
// the file from running, which is the same outcome as skipping them and is
// harder to notice, since the suite still reports passes.
var skipTokens = []string{
	"t.Skip(",
	"//go:build ignore",
	"// +build ignore",
	"t.Skipf(",
	"t.SkipNow(",
	"#[ignore]",
	"it.skip(",
	"test.skip(",
	"describe.skip(",
	"it.todo(",
	"test.todo(",
	"it.only(",
	"test.only(",
	"describe.only(",
	"xit(",
	"xdescribe(",
}

// workflowDir is the tree whose contents decide what CI runs at all. A change
// that edits it can turn off the checks that would have judged it.
const workflowDir = ".github/workflows"

// gitAttributes decides how git renders a diff. A `-diff` or `binary`
// attribute replaces a hunk body with "Binary files ... differ", and the
// attribute is read from the branch — so a change that edits this file is
// changing the evidence this package reads about itself. `--text` stops the
// rendering trick; this veto covers the rest of what the file can do.
const gitAttributes = ".gitattributes"

// testDeclarations open a test. Removing one is how a check stops failing
// without anything being fixed, which is the same "made a verdict go away"
// evidence a suppression is — so it is read from removed lines, and only in
// files that look like tests, where these tokens cannot mean anything else.
var testDeclarations = []string{
	"func Test",
	"func Benchmark",
	"func Fuzz",
	"#[test]",
	"it(",
	"test(",
	"describe(",
}

// Disqualifications returns every veto the change trips, one per kind per
// file, in the order the change presents them.
//
// It returns all of them rather than stopping at the first, because the
// report is meant to be actionable: an author who removes one suppression
// only to be referred again for a second has learnt nothing from the first
// run. It collapses repeats within a file for the opposite reason — a file
// that adds forty suppressions has one problem, and forty rows saying so
// would bury the verdict.
//
// A suppression rewritten by a formatter counts as newly added, since the
// diff shows it as a removal and an addition. That is fail-closed on purpose
// — the cost is an occasional referral for a change that only moved a line,
// and the alternative is matching removals against additions to decide which
// are "really" new, which is a heuristic an agent can aim at.
func Disqualifications(ch Change, extra Disqualifiers) []Disqualification {
	var out []Disqualification
	seen := map[[2]string]bool{}
	add := func(kind, p, evidence string) {
		key := [2]string{kind, p}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Disqualification{Kind: kind, Path: p, Evidence: evidence})
	}

	for _, line := range ch.Added {
		if tok, ok := containsAny(line.Text, suppressionTokens); ok {
			add("suppression added", line.Path, fmt.Sprintf("%s introduces %s", line.Path, tok))
		}
		if tok, ok := containsAny(line.Text, skipTokens); ok {
			add("test disabled", line.Path, fmt.Sprintf("%s introduces %s", line.Path, tok))
		}
	}
	for _, p := range ch.Deleted {
		if IsTestPath(p) {
			add("tests removed", p, p+" deleted")
		}
	}
	for _, line := range ch.Removed {
		if !IsTestPath(line.Path) {
			continue
		}
		if tok, ok := containsAny(line.Text, testDeclarations); ok {
			add("tests removed", line.Path, fmt.Sprintf("%s drops %s", line.Path, tok))
		}
	}
	for _, p := range ch.Paths {
		switch {
		case path.Base(p) == config.FileName || path.Base(p) == FileName:
			add("lydite config edited", p, p)
		case path.Base(p) == gitAttributes:
			add("diff rendering edited", p, p)
		case p == workflowDir || strings.HasPrefix(p, workflowDir+"/"):
			add("CI workflow edited", p, p)
		}
		for _, pattern := range extra.Paths {
			if Match(pattern, p) {
				add("declared disqualifying path", p, fmt.Sprintf("%s matches %s", p, pattern))
				break
			}
		}
	}
	return out
}

// containsAny finds the first token present in text as a whole word.
//
// A bare substring search is not good enough, and the failure is not
// hypothetical: "xit(" occurs inside "os.Exit(", so every Go file that exits
// a process would be reported as disabling a test. A disqualifier that fires
// on ordinary code is worse than one that occasionally misses, because a tag
// readers learn to ignore stops working for the cases it was built for.
func containsAny(text string, tokens []string) (string, bool) {
	for _, t := range tokens {
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], t)
			if i < 0 {
				break
			}
			if atWordStart(text, from+i) {
				return t, true
			}
			from += i + 1
		}
	}
	return "", false
}

// atWordStart reports whether the match at i begins a token rather than
// continuing an identifier. Tokens that already start with punctuation
// ("#nosec", "#[allow(") carry their own boundary and need no check.
func atWordStart(text string, i int) bool {
	if i == 0 {
		return true
	}
	if !isIdentByte(text[i]) {
		return true
	}
	return !isIdentByte(text[i-1])
}

func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
