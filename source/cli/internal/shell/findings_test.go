package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// captured is the pinned ShellCheck's json1 report over the two scripts
// captureTree writes, run as `shellcheck --format=json1 ./scripts/a.sh
// ./b.bash`. It reports b.bash first although it was handed a.sh first.
const captured = `{"comments":[` +
	`{"file":"./b.bash","line":2,"endLine":2,"column":1,"endColumn":9,"level":"warning","code":2164,"message":"Use 'cd ... || exit' or 'cd ... || return' in case cd fails.","fix":null},` +
	`{"file":"./b.bash","line":2,"endLine":2,"column":4,"endColumn":9,"level":"info","code":2086,"message":"Double quote to prevent globbing and word splitting.","fix":null},` +
	`{"file":"./scripts/a.sh","line":2,"endLine":2,"column":17,"endColumn":19,"level":"info","code":2086,"message":"Double quote to prevent globbing and word splitting.","fix":null},` +
	`{"file":"./scripts/a.sh","line":2,"endLine":2,"column":22,"endColumn":24,"level":"info","code":2086,"message":"Double quote to prevent globbing and word splitting.","fix":null}` +
	`]}`

// captureTree writes the scripts captured was taken over. a.sh's second line
// opens with a tab and carries a two-byte character between its two
// diagnostics, so a column read as bytes, or a tab read as eight, cuts in the
// wrong place.
func captureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "scripts/a.sh", "#!/bin/sh\n\tTOKEN=abc echo $1 ü $2\necho ok\n")
	write(t, dir, "b.bash", "#!/bin/bash\ncd $HOME\n")
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func site(rule, prefix string) string { return rule + separator + prefix }

func TestEveryCommentIsALocatedClaim(t *testing.T) {
	dir := captureTree(t)
	r := result(dir, executil.Result{Name: Gate, Output: captured, Err: errors.New("exit status 1")})

	if len(r.Findings) != 4 {
		t.Fatalf("findings = %+v, want four", r.Findings)
	}
	f := r.Findings[2]
	if f.Gate != Gate || f.Path != "scripts/a.sh" || f.Line != 2 || f.EndLine != 0 ||
		f.Rule != "SC2086" || f.Severity != "info" ||
		f.Message != "Double quote to prevent globbing and word splitting." {
		t.Errorf("finding = %+v", f)
	}
	if len(f.Detail) != 1 || f.Detail[0] != "https://www.shellcheck.net/wiki/SC2086" {
		t.Errorf("detail = %v, want the rule's wiki page", f.Detail)
	}
	if f.Component != "" {
		t.Errorf("component = %q, want it left to the caller", f.Component)
	}
}

// Every claim on a line is cut at the earliest column reported for that line.
// Cut at its own, the second SC2086 on a.sh's line would carry `$1 ü` — the
// first claim's text — as its own identity.
func TestEveryClaimOnALineIsCutAtItsEarliestColumn(t *testing.T) {
	dir := captureTree(t)
	r := result(dir, executil.Result{Name: Gate, Output: captured, Err: errors.New("exit status 1")})

	want := map[string][]string{
		"b.bash":       {site("SC2164", ""), site("SC2086", "")},
		"scripts/a.sh": {site("SC2086", "TOKEN=abc echo"), site("SC2086", "TOKEN=abc echo")},
	}
	got := map[string][]string{}
	for _, f := range r.Findings {
		got[f.Path] = append(got[f.Path], f.Site)
		if strings.Contains(f.Site, "$1") || strings.Contains(f.Site, "ü") {
			t.Errorf("site %q carries text an earlier claim on the line fired on", f.Site)
		}
	}
	for path, sites := range want {
		if strings.Join(got[path], "|") != strings.Join(sites, "|") {
			t.Errorf("%s sites = %q, want %q", path, got[path], sites)
		}
	}
}

// Two claims alike in rule and site on one line are told apart by the ordinal
// alone, and it is counted in source order whatever order ShellCheck reported.
func TestClaimsAreNumberedInSourceOrder(t *testing.T) {
	dir := captureTree(t)
	r := result(dir, executil.Result{Name: Gate, Output: captured, Err: errors.New("exit status 1")})

	var paths []string
	for _, f := range r.Findings {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "b.bash,b.bash,scripts/a.sh,scripts/a.sh" {
		t.Errorf("paths = %v, want source order by file", paths)
	}
	a0, a1 := r.Findings[2], r.Findings[3]
	if a0.Ordinal != 0 || a1.Ordinal != 1 || a0.Fingerprint() == a1.Fingerprint() {
		t.Errorf("ordinals = %d, %d, fingerprints %s %s: two claims collapsed into one", a0.Ordinal, a1.Ordinal, a0.Fingerprint(), a1.Fingerprint())
	}

	reversed := `{"comments":[` + strings.Join(reverse(splitComments(captured)), ",") + `]}`
	again := result(dir, executil.Result{Name: Gate, Output: reversed, Err: errors.New("exit status 1")})
	for i := range r.Findings {
		if r.Findings[i].Fingerprint() != again.Findings[i].Fingerprint() {
			t.Errorf("finding %d identified differently when reported in another order", i)
		}
	}
}

func splitComments(doc string) []string {
	body := strings.TrimSuffix(strings.TrimPrefix(doc, `{"comments":[`), `]}`)
	parts := strings.Split(body, "},{")
	for i := range parts {
		parts[i] = "{" + strings.Trim(parts[i], "{}") + "}"
	}
	return parts
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// A reindented line identifies the same claim: the cut is normalised.
func TestReindentingALineDoesNotReidentifyItsClaim(t *testing.T) {
	flat, deep := t.TempDir(), t.TempDir()
	write(t, flat, "x.sh", "echo $1\n")
	write(t, deep, "x.sh", "        echo    $1\n")
	one := findings(flat, report{Comments: []comment{{File: "./x.sh", Line: 1, Column: 6, Code: 2086}}})
	two := findings(deep, report{Comments: []comment{{File: "./x.sh", Line: 1, Column: 17, Code: 2086}}})

	if one[0].Site != site("SC2086", "echo") || one[0].Fingerprint() != two[0].Fingerprint() {
		t.Errorf("sites %q and %q, want one identity for a reindented line", one[0].Site, two[0].Site)
	}
}

// A column past the line, a line past the file, and a path the tree does not
// hold all answer an empty prefix rather than failing or reading elsewhere.
func TestAnUnreadableSiteIsEmptyRatherThanAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.sh", "echo $1\n")
	src := newTree(dir)
	if got := src.prefix("x.sh", 1, 99); got != "echo $1" {
		t.Errorf("prefix past the line = %q, want the whole line", got)
	}
	for _, tc := range []struct {
		path string
		line int
	}{{"x.sh", 9}, {"x.sh", 0}, {"missing.sh", 1}, {"../outside.sh", 1}} {
		if got := src.prefix(tc.path, tc.line, 3); got != "" {
			t.Errorf("prefix(%q, %d) = %q, want empty", tc.path, tc.line, got)
		}
	}
}

// A site never grows past the bound, however long the line before the cut.
func TestASiteIsBounded(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("a", 3*maxSiteRunes)
	write(t, dir, "x.sh", long+" $1\n")
	got := newTree(dir).prefix("x.sh", 1, len(long)+2)
	if len([]rune(got)) != maxSiteRunes {
		t.Errorf("prefix is %d runes, want %d", len([]rune(got)), maxSiteRunes)
	}
}

func TestACommentThatLocatesNothingIsNoClaim(t *testing.T) {
	got := findings(t.TempDir(), report{Comments: []comment{
		{File: "", Line: 1, Column: 1, Code: 2086},
		{File: "./x.sh", Line: 0, Column: 1, Code: 2086},
	}})
	if len(got) != 0 {
		t.Errorf("findings = %+v, want none", got)
	}
}

func TestACleanRunPassesWithNoClaims(t *testing.T) {
	r := result(t.TempDir(), executil.Result{Name: Gate, Output: `{"comments":[]}`})
	if !r.Ok() || len(r.Findings) != 0 || r.Detail != "" {
		t.Errorf("result = %+v, want a clean pass", r)
	}
}

// A failing row renders its claims, because the JSON run is the only run and
// nothing else reaches a reader.
func TestAFailingRunRendersItsClaims(t *testing.T) {
	dir := captureTree(t)
	r := result(dir, executil.Result{Name: Gate, Output: captured, Err: errors.New("exit status 1")})
	if r.Ok() {
		t.Fatal("a run with diagnostics passed")
	}
	for _, want := range []string{"scripts/a.sh:2  SC2086  info", "b.bash:2  SC2164  warning", "https://www.shellcheck.net/wiki/SC2164"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, r.Detail)
		}
	}
}

// A file ShellCheck could not open locates nothing and is named only on
// stderr, which the row carries beside whatever it did locate.
func TestAFileShellCheckCouldNotReadIsNamed(t *testing.T) {
	r := result(t.TempDir(), executil.Result{
		Name:   Gate,
		Output: `{"comments":[]}`,
		Stderr: "./gone.sh: ./gone.sh: openBinaryFile: does not exist (No such file or directory)\n",
		Err:    errors.New("exit status 2"),
	})
	if r.Ok() || !strings.Contains(r.Detail, "gone.sh") {
		t.Errorf("result = %+v, want a failure naming the unreadable file", r)
	}
}

// A clean exit beside a report that does not parse is a run nothing shows
// checked anything, and it fails rather than passing on the status alone.
func TestACleanExitWithAnUnreadableReportFails(t *testing.T) {
	r := result(t.TempDir(), executil.Result{Name: Gate, Output: "not json"})
	if r.Ok() || r.Detail == "" {
		t.Errorf("result = %+v, want a failure with a reason", r)
	}
}

func TestAFailureThatSaysNothingStatesItsStatus(t *testing.T) {
	r := result(t.TempDir(), executil.Result{Name: Gate, Output: `{"comments":[]}`, Err: errors.New("exit status 4")})
	if r.Ok() || !strings.Contains(r.Detail, "exit status 4") {
		t.Errorf("detail = %q, want the status stated", r.Detail)
	}
}

// The site and the findings package agree on what a site is made of: the
// rule, the separator, and normalised text.
func TestASiteIsNormalised(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.sh", "a\t\tb   c $1\n")
	got := newTree(dir).prefix("x.sh", 1, 10)
	if got != finding.Normalise("a\t\tb   c") {
		t.Errorf("prefix = %q", got)
	}
}
