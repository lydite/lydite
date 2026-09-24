package referral

import (
	"fmt"
	"path"
	"strings"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/pathmatch"
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

// Kinds a caller supplies rather than this package computing them.
//
// Each is read off something the line-level diff evidence Disqualifications
// is given does not carry: a public-API diff, the author's own declaration,
// the text of a manifest at two revisions. The caller that has it appends the
// Disqualification; the names live here so the two sides spell the same
// label, and so a reader of this file sees every kind a report can carry.
const (
	// DisqualificationAPIBreakDeclared is a change whose author declared a
	// breaking API change. The claim may only ever add a referral, so it is
	// honoured with no corroboration from the surface diff and has no power
	// over the undeclared-break gate (see docs/adr/0040).
	DisqualificationAPIBreakDeclared = "api break declared"
	// DisqualificationAPISurfaceUncomputable is a component whose exported
	// API could not be compared at all. Neither pass nor fail is true of it:
	// failing is a gate the author cannot clear, since the merge-base's tree
	// is not theirs to fix, and passing is a gate that could not run
	// rendering as one that ran and found nothing.
	DisqualificationAPISurfaceUncomputable = "api surface could not be compared"
	// DisqualificationDependencyAdded is a change that pins a package the
	// merge-base did not, direct or transitive. Adding a dependency is not
	// wrong, which is why it refers rather than fails: there is no further
	// work the author could do to clear it, and new untrusted code entering
	// the tree is what an advisory database has nothing to say about (see
	// docs/adr/0047).
	DisqualificationDependencyAdded = "dependency added"
	// DisqualificationDependencyDeltaUnmeasured is a manifest whose
	// dependency set could not be built at one of the two sides — an
	// ecosystem with no reader, or content that did not parse. "lydite does
	// not know whether this change added a dependency" and "it did not" must
	// not reach the same verdict, because the first is where a malformed or
	// novel lockfile lands and the second is where routine maintenance does.
	DisqualificationDependencyDeltaUnmeasured = "dependency delta could not be measured"
)

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
	// Every lydite declaration, by the prefix they share: a mutant nobody
	// can kill, a function whose coverage is taken in another process. Each
	// is asserted by its author and checkable by nobody, so clearing the gate
	// merges unattended and declaring it unclearable puts a human on the
	// claim. The prefix is imported rather than respelled, and read instead
	// of the individual tokens — a list of those here would go stale the
	// first time a gate is added, silently, in the one place where a missed
	// suppression means a change merges unread.
	annotation.Prefix,
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

// substringSuppressionTokens are suppression markers whose own tool reads
// them by plain substring, so a word boundary here would open exactly the
// gap this package exists to close.
//
// gitleaks reads `gitleaks:allow` with an unanchored match: `# xgitleaks:allow`
// clears a secret finding for gitleaks exactly as `# gitleaks:allow` does,
// because gitleaks does not require the token to start a word. Checking it
// with containsAny's word-boundary rule would let a change spell the marker
// with a leading identifier character and merge unattended — the marker is
// suppressing gitleaks's own reading of the line, not lydite's, so it is
// lydite that has to match gitleaks's rule and not its own.
var substringSuppressionTokens = []string{
	"gitleaks:allow",
}

// shellcheckDirectiveName is how Disqualifications' evidence names a
// ShellCheck directive, which has no one fixed spelling to quote.
//
// ShellCheck reads a directive as `#`, any run of spaces and tabs,
// `shellcheck`, then at least one more — so `# shellcheck disable=SC2086`,
// `#shellcheck disable=all` and `#  shellcheck  source=/dev/null` are all
// directives, and no single literal token matches every one of them. Every
// directive is vetoed, not only `disable=`: `source=/dev/null` is ShellCheck's
// own documented way to silence a missing-source warning, `shell=` changes the
// dialect and with it which checks apply, and `extended-analysis=false` turns
// the dataflow checks off. A list of the narrowing keys would go stale the
// first time ShellCheck added one, and the cost of vetoing the rest is a
// referral for a change a person then reads.
const shellcheckDirectiveName = "a shellcheck directive"

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

// gitleaksConfig and gitleaksIgnore are gitleaks' own suppression surfaces.
// lydite passes no --config, so gitleaks discovers both at whatever
// directory it is told to scan — matched by base name rather than a
// repository-root path, the same way gitAttributes is, since the scan root
// referral runs over is not always the repository root. An allowlist broad
// enough to match every path, or a fingerprint an author has decided is not
// a secret, switches the gate off exactly as removing a #nosec would — and
// neither is a token any one line can be checked against, so the file
// itself is the veto.
const gitleaksConfig = ".gitleaks.toml"
const gitleaksIgnore = ".gitleaksignore"

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
		} else if tok, ok := containsSubstring(line.Text, substringSuppressionTokens); ok {
			add("suppression added", line.Path, fmt.Sprintf("%s introduces %s", line.Path, tok))
		} else if containsShellcheckDirective(line.Text) {
			add("suppression added", line.Path, fmt.Sprintf("%s introduces %s", line.Path, shellcheckDirectiveName))
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
	// A rename out of a test path takes the file out of the test runner's
	// view exactly as a deletion does, and leaves nothing in Deleted or in
	// Removed to notice: `git mv foo_test.go foo_disabled.go` produces a
	// rename record and a patch with no hunks at all.
	for _, r := range ch.Renamed {
		if IsTestPath(r.From) && !IsTestPath(r.To) {
			add("tests removed", r.From, r.From+" renamed to "+r.To)
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
		case InConfigDir(p):
			add("lydite config edited", p, p)
		case path.Base(p) == gitAttributes:
			add("diff rendering edited", p, p)
		case path.Base(p) == gitleaksConfig || path.Base(p) == gitleaksIgnore:
			add("secret-scan config edited", p, p)
		case p == workflowDir || strings.HasPrefix(p, workflowDir+"/"):
			add("CI workflow edited", p, p)
		}
		for _, pattern := range extra.Paths {
			if pathmatch.Match(pattern, p) {
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

// containsSubstring finds the first token present in text anywhere, with no
// word-boundary check.
//
// For a marker whose own tool reads it the same unanchored way — gitleaks'
// `gitleaks:allow` — a word-boundary requirement here would accept a
// spelling the tool itself still honours, which is the gap containsAny's
// stricter check exists to close for tokens that are Go, Rust or TypeScript
// syntax.
func containsSubstring(text string, tokens []string) (string, bool) {
	for _, t := range tokens {
		if strings.Contains(text, t) {
			return t, true
		}
	}
	return "", false // [lydite:exclude_from_mutation][the token beside a false
	// second value is read by nobody: every caller checks the bool and never
	// looks at the string when it is false, so no caller can be shown a
	// different one]
}

// containsShellcheckDirective reports whether text carries a ShellCheck
// directive anywhere in it: a `#`, any spaces or tabs, `shellcheck`, and at
// least one space or tab after it, which is the prefix ShellCheck's own
// directive reader requires before it reads any key.
//
// The `#` is not required to start a word. A `#` inside a shell word is not a
// comment, so ShellCheck would not read one there, but a veto that fires on a
// spelling nothing honours costs a referral, and one that misses a spelling
// something does honour merges a suppression unread.
func containsShellcheckDirective(text string) bool {
	const name = "shellcheck"
	for from := 0; ; {
		i := strings.IndexByte(text[from:], '#')
		if i < 0 {
			return false
		}
		rest := strings.TrimLeft(text[from+i+1:], " \t")
		if after, ok := strings.CutPrefix(rest, name); ok && after != "" && (after[0] == ' ' || after[0] == '\t') {
			return true
		}
		from += i + 1
	}
}

// atWordStart reports whether the match at i begins a token rather than
// continuing an identifier or a selector. Tokens that already start with
// punctuation ("#nosec", "#[allow(") carry their own boundary and need no
// check.
//
// A preceding "." disqualifies the match for the same reason a preceding
// letter does: "test(" is a test declaration at the start of a statement and
// a method call in "re.test(x)", and a veto that fires on ordinary code
// teaches readers to ignore it. Every token here that legitimately contains
// a selector carries it in the token itself ("t.Skip(", "it.skip("), so the
// forms lydite means to catch are unaffected.
func atWordStart(text string, i int) bool {
	if i == 0 {
		return true
	}
	if !isIdentByte(text[i]) {
		return true
	}
	return !isIdentByte(text[i-1]) && text[i-1] != '.'
}

func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
