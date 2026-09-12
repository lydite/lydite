package golang

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/fixture"
)

// gosecFixture is one of gosec's real `-fmt json` reports, captured from the
// pinned tool, with the tree it names materialised beside it.
//
// The absolute paths the report states are templated: gosec names files by the
// absolute path it scanned them at, so a captured report would otherwise only
// rebase on the machine that produced it.
func gosecFixture(t *testing.T, name string) (gosecReport, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	dir := fixture.Tree(t, "testdata/gosecprobe")
	var rep gosecReport
	templated := strings.ReplaceAll(string(data), "{{DIR}}", dir)
	if err := json.Unmarshal([]byte(templated), &rep); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return rep, dir
}

func TestGosecReadsTheRealReport(t *testing.T) {
	rep, dir := gosecFixture(t, "gosec-report.json")
	got := gosecFindings(dir, rep)
	if len(got) != 3 {
		t.Fatalf("got %d claims, want 3", len(got))
	}
	first := got[0]
	if first.Gate != "gosec" {
		t.Errorf("gate = %q, want gosec", first.Gate)
	}
	if first.Path != "main.go" {
		t.Errorf("path = %q, want main.go — gosec states an absolute path and it is rebased onto the component", first.Path)
	}
	if first.Rule != "G703" || first.Line != 11 {
		t.Errorf("got %s at line %d, want G703 at 11", first.Rule, first.Line)
	}
	if first.Severity != "high" {
		t.Errorf("severity = %q, want high", first.Severity)
	}
	if len(first.Detail) != 3 {
		t.Errorf("detail = %q, want gosec's three-line excerpt", first.Detail)
	}
	if !strings.Contains(first.Site, "os.ReadFile") {
		t.Errorf("site = %q, want the rule with the text it fired on", first.Site)
	}
}

func TestGosecSiteIsTheFlaggedLineAndNotTheExcerpt(t *testing.T) {
	// Two rules firing on one line must be two claims, and one rule's claim
	// must not move because a neighbouring line changed. gosec's own `code`
	// excerpt spans three lines and carries its line-number prefixes, so a
	// site built from it would do both wrong.
	rep, dir := gosecFixture(t, "gosec-report.json")
	got := gosecFindings(dir, rep)
	for _, f := range got {
		if strings.Contains(f.Site, "\n") || strings.Contains(f.Site, "10: ") {
			t.Errorf("site = %q, want one normalised line", f.Site)
		}
	}
}

func TestGosecDoesNotReadABuildFailureAsACleanPass(t *testing.T) {
	// A package that did not compile is a package gosec scanned nothing in,
	// and it says so in the report rather than in its exit status.
	rep, dir := gosecFixture(t, "gosec-build-errors.json")
	if len(rep.BuildErrors) == 0 {
		t.Fatal("fixture names no build errors")
	}
	errs := buildErrors(dir, rep)
	if len(errs) == 0 {
		t.Fatal("buildErrors found none, so a failure to compile would render as a passing row")
	}
	for _, e := range errs {
		if !strings.Contains(e, "UndefinedType") {
			t.Errorf("build error %q does not say what failed", e)
		}
	}
}

func TestGosecSaysEachBuildFailureOnce(t *testing.T) {
	// The report repeats the package-level message under the empty key as
	// well as under the file, so the same failure arrives twice.
	rep, dir := gosecFixture(t, "gosec-build-errors.json")
	if len(rep.BuildErrors) < 2 {
		t.Fatal("fixture does not hold the repeat this guards")
	}
	got := buildErrors(dir, rep)
	if len(got) != 1 {
		t.Fatalf("buildErrors = %q, want the one failure said once", got)
	}
	// The located entry, named from the component. The report keys these by
	// the absolute path gosec scanned at, and the aggregate under the empty
	// key — reading one for the other would put a bare ":0:" where a file
	// should be, and the developer's own checkout path into the comment.
	if !strings.HasPrefix(got[0], "main.go:") {
		t.Errorf("buildErrors = %q, want the located entry named from the component", got)
	}
}

func TestGosecSpan(t *testing.T) {
	cases := []struct {
		in                 string
		wantStart, wantEnd int
	}{
		{"11", 11, 0},
		{"10-12", 10, 12},
		{" 10 - 12 ", 10, 12},
		// A range that ends where it starts is one line, and the extent is
		// dropped rather than recorded as a span of zero length.
		{"10-10", 10, 0},
		// A malformed field costs the claim its extent, never its location.
		{"10-", 10, 0},
		{"10-nonsense", 10, 0},
		// An unparseable start is what makes the claim unanchorable, rather
		// than anchoring it to line zero.
		{"", 0, 0},
		{"nonsense", 0, 0},
	}
	for _, tc := range cases {
		start, end := gosecSpan(tc.in)
		if start != tc.wantStart || end != tc.wantEnd {
			t.Errorf("gosecSpan(%q) = %d, %d, want %d, %d", tc.in, start, end, tc.wantStart, tc.wantEnd)
		}
	}
}

func TestGosecSkipsAnIssueThatLocatesNothing(t *testing.T) {
	rep := gosecReport{Issues: []gosecIssue{{RuleID: "G000", File: "", Line: "nonsense"}}}
	if got := gosecFindings(t.TempDir(), rep); len(got) != 0 {
		t.Fatalf("got %d claims, want none — a claim on line zero is one nothing can anchor", len(got))
	}
}

func TestRelToKeepsAPathInsideTheComponent(t *testing.T) {
	cases := []struct {
		name, dir, file, want string
	}{
		{"rebased onto the component", "/a/b", "/a/b/c/d.go", "c/d.go"},
		{"a path outside the component is not rewritten with ..", "/a/b", "/a/z/d.go", "/a/z/d.go"},
		{"an empty path stays empty", "/a/b", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relTo(tc.dir, tc.file); got != tc.want {
				t.Errorf("relTo(%q, %q) = %q, want %q", tc.dir, tc.file, got, tc.want)
			}
		})
	}
}

func TestGosecFallsBackToTheExitStatusOnAnUnreadableReport(t *testing.T) {
	// Inventing a verdict from an unreadable report would either fail a clean
	// run or pass a dirty one on the strength of a parse error.
	for _, data := range []string{"", "not json at all", `{"Issues": `} {
		if _, ok := parseGosec([]byte(data)); ok {
			t.Errorf("parseGosec(%q) reported success", data)
		}
	}
	if _, ok := parseGosec([]byte(`{"Issues":[]}`)); !ok {
		t.Error("a report that does parse was rejected")
	}
}

func TestRelToTreatsOnlyARealTraversalAsAnEscape(t *testing.T) {
	// The test is the `..` element and not the prefix `..`, which a file named
	// `..rc` beside the component also carries. Rejecting that would fall back
	// to the absolute path, which loses the anchor and puts the local
	// checkout's path into the published report.
	cases := []struct {
		name, dir, file, want string
	}{
		{"a dotted name is not a traversal", "/a/b", "/a/b/..rc", "..rc"},
		{"a dotted name in a subdirectory", "/a/b", "/a/b/c/..rc", "c/..rc"},
		{"a real traversal is refused", "/a/b", "/a/z/d.go", "/a/z/d.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relTo(tc.dir, tc.file); got != tc.want {
				t.Errorf("relTo(%q, %q) = %q, want %q", tc.dir, tc.file, got, tc.want)
			}
		})
	}
}

func TestGosecExplainsAPackageTheCompilerAttributedToNoFile(t *testing.T) {
	// A package whose failure has no file — a missing import path, a broken
	// module — is named only in the aggregate entry. The row fails for it
	// either way; what must not be lost is which package went unscanned.
	rep := gosecReport{BuildErrors: map[string][]gosecBuildError{
		"": {
			{Errorf: "# example.com/located\n./a.go:3:2: undefined: X"},
			{Errorf: "# example.com/unlocated\nno required module provides package example.com/missing"},
		},
		"/tmp/probe/a.go": {{Line: 3, Column: 2, Errorf: "undefined: X"}},
	}}
	got := buildErrors("/tmp/probe", rep)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "undefined: X") {
		t.Errorf("buildErrors = %q, want the located failure", got)
	}
	if !strings.Contains(joined, "example.com/unlocated") {
		t.Errorf("buildErrors = %q, want the package the compiler attributed to no file", got)
	}
	// The aggregate for the located package repeats what the located entry
	// already says, and a reader must not see it twice.
	if strings.Count(joined, "undefined: X") != 1 {
		t.Errorf("buildErrors = %q, want the located failure said once", got)
	}
}

func TestGosecArgvKeepsTheReportACopy(t *testing.T) {
	// `-stdout -verbose text` beside `-fmt json -out <file>` is what makes the
	// JSON a copy rather than a replacement. Without it the terminal and the
	// CI log see nothing, and a scanner's findings are the point.
	argv := gosecArgv("/tmp/report.json")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-fmt json", "-out /tmp/report.json", "-stdout", "-verbose text"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv = %q, want %q — without it the report replaces the terminal output", joined, want)
		}
	}
	// Findings in generated code are not actionable: the only fix is to the
	// generator, and a #nosec there is erased by the next regeneration.
	if !slices.Contains(argv, "-exclude-generated") {
		t.Errorf("argv = %q, want -exclude-generated", argv)
	}
	if argv[len(argv)-1] != "./..." {
		t.Errorf("argv = %q, want the package pattern last", argv)
	}
}

func TestExcerptDropsTheTrailingBlankLine(t *testing.T) {
	// A tool's quoted block ends in a newline, which would otherwise become an
	// empty final line and render as a blank row under every finding.
	if got := excerpt("10: a\n11: b\n"); len(got) != 2 {
		t.Errorf("excerpt = %q, want two lines", got)
	}
	if got := excerpt(""); got != nil {
		t.Errorf("excerpt = %q, want nothing for an empty block", got)
	}
	if got := excerpt("\n"); got != nil {
		t.Errorf("excerpt = %q, want nothing for a block that is only a newline", got)
	}
}

func TestMessageOfDropsTheLocationPrefix(t *testing.T) {
	// A located entry carries `file:line: ` in front of the compiler's own
	// line, and it is the message that is compared against an aggregate.
	// buildErrors writes that prefix onto every entry it passes here, which is
	// the whole of what makes cutting at the first ": " right.
	if got := messageOf("main.go:6: undefined: X"); got != "undefined: X" {
		t.Errorf("messageOf = %q", got)
	}
	// A string carrying no separator at all is its own message.
	if got := messageOf("undefined"); got != "undefined" {
		t.Errorf("messageOf = %q", got)
	}
}

func TestGosecNumbersTwoIssuesAlikeInPathAndSite(t *testing.T) {
	// gosec reports one rule twice on one line when a line holds two matching
	// expressions, and the site is the rule with that line's text — identical
	// for both. The ordinal is the whole of what keeps them two claims;
	// without it they hash alike and finding.Set drops the second.
	rep := gosecReport{Issues: []gosecIssue{
		{RuleID: "G401", File: "a.go", Line: "7", Details: "first"},
		{RuleID: "G401", File: "a.go", Line: "7", Details: "second"},
	}}
	got := gosecFindings(t.TempDir(), rep)
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	if got[0].Site != got[1].Site {
		t.Fatalf("the claims do not share a site, so this no longer guards the ordinal")
	}
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Error("the two claims share a fingerprint, so the second is dropped as a duplicate")
	}
}

func TestGosecKeepsAClaimOnTheFirstLine(t *testing.T) {
	// Line 1 is a real line. A boundary that excluded it would silently drop
	// every finding on the first line of every file.
	rep := gosecReport{Issues: []gosecIssue{{RuleID: "G501", File: "a.go", Line: "1", Details: "on line one"}}}
	got := gosecFindings(t.TempDir(), rep)
	if len(got) != 1 {
		t.Fatalf("got %d claims, want the one on line 1", len(got))
	}
	if got[0].Line != 1 {
		t.Errorf("line = %d, want 1", got[0].Line)
	}
	// Line 0 is not a line, and a claim carrying it can be anchored to
	// nothing and told from nothing.
	zero := gosecReport{Issues: []gosecIssue{{RuleID: "G501", File: "a.go", Line: "0"}}}
	if got := gosecFindings(t.TempDir(), zero); len(got) != 0 {
		t.Errorf("got %d claims for line 0, want none", len(got))
	}
}

func TestRelToRebasesAgainstARelativeComponentDirectory(t *testing.T) {
	// `lydite scan --dir .` builds a component directory that is relative
	// while gosec always reports an absolute path, and Rel refuses a pair
	// that is not both one or both the other. Without resolving the directory
	// first, every claim keeps the absolute path of the machine that produced
	// it — which anchors nothing and publishes somebody's checkout path.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got := relTo("testdata", filepath.Join(cwd, "testdata", "gosecprobe", "main.go"))
	if got != "gosecprobe/main.go" {
		t.Errorf("relTo = %q, want the path rebased onto the relative directory", got)
	}
}

func TestRelToRebasesThroughASymlinkedComponentDirectory(t *testing.T) {
	// A scan root given as a link and reported through its target shares no
	// prefix at all — macOS makes /tmp a link to /private/tmp, which is a real
	// configuration rather than a broken one. Resolving both is what rebases
	// the claim instead of leaving it absolute.
	target := t.TempDir()
	file := filepath.Join(target, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this platform will not make a symlink: %v", err)
	}
	if got := relTo(link, file); got != "a.go" {
		t.Errorf("relTo = %q, want the path rebased through the link", got)
	}
}

func TestResolvedRootWithoutASymlinkToResolve(t *testing.T) {
	// A directory that does not exist has no resolved form, and a path is
	// rebased against the absolute form alone rather than against an empty
	// string — which would match every path and rebase all of them wrongly.
	missing := filepath.Join(t.TempDir(), "absent")
	root := resolvedRoot(missing)
	if root.real != "" {
		t.Errorf("real = %q, want none for a directory that cannot be resolved", root.real)
	}
	if got := root.rel(filepath.Join(missing, "a.go")); got != "a.go" {
		t.Errorf("rel = %q, want the path rebased against the absolute form", got)
	}
	// A path outside it is still refused rather than rewritten with "..".
	if got := root.rel("/elsewhere/b.go"); got != "/elsewhere/b.go" {
		t.Errorf("rel = %q, want the path left alone", got)
	}
}

func TestBuildErrorsReadsTheAggregateWhenNoFailureHasAFile(t *testing.T) {
	// Every failure the compiler attributed to no file at all is named under
	// the empty key. Reading only the file-keyed entries would leave such a
	// package unexplained while the row still failed for it.
	rep := gosecReport{BuildErrors: map[string][]gosecBuildError{
		"": {{Errorf: "# example.com/m\nno required module provides package example.com/missing"}},
	}}
	got := buildErrors(t.TempDir(), rep)
	if len(got) != 1 {
		t.Fatalf("buildErrors = %q, want the aggregate entry", got)
	}
	if !strings.Contains(got[0], "example.com/missing") {
		t.Errorf("buildErrors = %q, want the failure named", got)
	}
	// An entry carrying no message at all is not a failure to report.
	blank := gosecReport{BuildErrors: map[string][]gosecBuildError{"": {{Errorf: "   "}}}}
	if got := buildErrors(t.TempDir(), blank); len(got) != 0 {
		t.Errorf("buildErrors = %q, want none for an empty message", got)
	}
}

func TestGosecResultLeavesTheVerdictAloneWithNoReport(t *testing.T) {
	// A report lydite cannot read or parse leaves the run entirely to gosec's
	// own exit status. Inventing a verdict from it would either fail a clean
	// run or pass a dirty one on the strength of a read error.
	failing := executil.Result{Name: "gosec", Err: errors.New("exit status 1"), Output: "gosec said so"}
	missing := gosecResult(failing, t.TempDir(), filepath.Join(t.TempDir(), "absent.json"))
	if missing.Err == nil || missing.Output != "gosec said so" {
		t.Errorf("a missing report changed the verdict: %+v", missing)
	}
	if len(missing.Findings) != 0 {
		t.Errorf("a missing report produced %d claims", len(missing.Findings))
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gosecResult(failing, t.TempDir(), bad); got.Err == nil {
		t.Error("an unparseable report cleared gosec's own failure")
	}
}

func TestGosecResultFailsACleanRunThatCompiledNothing(t *testing.T) {
	// gosec exits non-zero for this today, so the branch changes no verdict
	// lydite currently reaches. It is here because a clean exit beside an
	// empty scan is the failure it exists to refuse.
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	report := `{"Golang errors":{"":[{"line":0,"column":0,"error":"# m\nbroken"}]},"Issues":[],"Stats":{}}`
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	got := gosecResult(executil.Result{Name: "gosec"}, dir, path)
	if got.Err == nil {
		t.Fatal("a package that did not compile read as a clean pass")
	}
	if !strings.Contains(got.Detail, "broken") {
		t.Errorf("detail = %q, want the compiler's own words — a verdict lydite invented must say why", got.Detail)
	}

	// A report naming no build error leaves a passing run passing.
	clean := filepath.Join(dir, "clean.json")
	if err := os.WriteFile(clean, []byte(`{"Golang errors":{},"Issues":[],"Stats":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gosecResult(executil.Result{Name: "gosec"}, dir, clean); got.Err != nil {
		t.Errorf("a clean report failed the row: %v", got.Err)
	}
}
