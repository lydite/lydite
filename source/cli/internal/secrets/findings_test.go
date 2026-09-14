package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/fixture"
)

// captured is one of the reports the pinned gitleaks wrote over the tree beside
// it, with that tree materialised.
//
// The probe files are committed with a `.txt` suffix and written back under
// their real names, as every fixture reproducing a finding here is: they hold
// invented credential-shaped strings, and lydite's own scan reads its testdata.
func captured(t *testing.T, name string) (dir string, rep report) {
	t.Helper()
	probes := map[string]string{
		"gitleaks":         "glprobe",
		"gitleaks-shifted": "glprobe-shifted",
	}
	probe, ok := probes[name]
	if !ok {
		t.Fatalf("no probe tree for %s", name)
	}
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return fixture.Tree(t, filepath.Join("testdata", probe)), rep
}

func TestReadsTheRealReport(t *testing.T) {
	dir, rep := captured(t, "gitleaks")
	got, unplaced := findings(dir, rep)
	if len(unplaced) != 0 {
		t.Errorf("unplaced = %q, want none — every leak in the capture names a file and a line", unplaced)
	}
	if len(got) != 5 {
		t.Fatalf("got %d claims, want the capture's 5", len(got))
	}
	// Source order, whatever order gitleaks reported in.
	type where struct {
		path string
		line int
	}
	want := []where{{"app.py", 2}, {"app.py", 3}, {"app.py", 4}, {"config.yml", 3}, {"config.yml", 4}}
	for i, w := range want {
		if got[i].Path != w.path || got[i].Line != w.line {
			t.Errorf("claim %d = %s:%d, want %s:%d", i, got[i].Path, got[i].Line, w.path, w.line)
		}
	}
	f := got[0]
	if f.Gate != "gitleaks" {
		t.Errorf("gate = %q, want gitleaks", f.Gate)
	}
	if f.Component != "" {
		t.Errorf("component = %q, want none — this gate is root-scoped", f.Component)
	}
	if f.Rule != "generic-api-key" {
		t.Errorf("rule = %q, want gitleaks' own rule id", f.Rule)
	}
	if f.EndLine != 0 {
		t.Errorf("EndLine = %d, want none for a claim about one line", f.EndLine)
	}
	if len(f.Detail) != 0 {
		t.Errorf("Detail = %q, want none — everything gitleaks would put there is a field already or is the secret", f.Detail)
	}
}

func TestEveryMessageSaysToRotate(t *testing.T) {
	// The author of a pull request reads Message and nothing else. gitleaks'
	// description ends at what it found, and an author who deletes the line has
	// fixed nothing.
	dir, rep := captured(t, "gitleaks")
	got, _ := findings(dir, rep)
	for _, f := range got {
		if !strings.HasSuffix(f.Message, rotate) {
			t.Errorf("message = %q, want it to end with the instruction to rotate", f.Message)
		}
		if !strings.HasPrefix(f.Message, "Detected a Generic API Key") {
			t.Errorf("message = %q, want gitleaks' own description first", f.Message)
		}
	}
}

func TestMessageNormalisesTheDescriptionsFullStop(t *testing.T) {
	cases := []struct {
		name string
		leak leak
		want string
	}{
		{"a description already ending in one", leak{RuleID: "r", Description: "Detected a key."}, "Detected a key. " + rotate},
		{"a description ending without one", leak{RuleID: "r", Description: "Detected a key"}, "Detected a key. " + rotate},
		{"a description ending in several", leak{RuleID: "r", Description: "Detected a key..."}, "Detected a key. " + rotate},
		{"surrounding space", leak{RuleID: "r", Description: "  Detected a key.  "}, "Detected a key. " + rotate},
		{"no description names the rule", leak{RuleID: "some-rule"}, "gitleaks matched some-rule. " + rotate},
		{"no description and no rule", leak{}, "gitleaks matched an unnamed rule. " + rotate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := message(tc.leak); got != tc.want {
				t.Errorf("message = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSiteIsTheLinePrefixAndNeverTheSecret(t *testing.T) {
	dir, rep := captured(t, "gitleaks")
	got, _ := findings(dir, rep)

	// Every invented credential in the probe tree, read back out of the
	// materialised files: none of them may appear in a site, which travels in
	// scan.json and into a comment on a public pull request.
	for _, f := range got {
		for _, secret := range secretsIn(t, filepath.Join(dir, filepath.FromSlash(f.Path))) {
			if strings.Contains(f.Site, secret) {
				t.Errorf("site for %s:%d quotes the secret it found", f.Path, f.Line)
			}
		}
	}

	want := map[string]string{
		// `    connect(secret_key="…"` — the assignment context, which is what
		// tells two secrets in one file apart.
		"app.py:2": site("generic-api-key", "connect("),
		// `    token = "…"` twice: the match begins at the assignment, so the
		// prefix is the indentation alone and normalises away. The ordinal is
		// what keeps these two claims.
		"app.py:3": site("generic-api-key", ""),
		"app.py:4": site("generic-api-key", ""),
		// `aws_key: "…"` — the match begins the line, so the site is the rule
		// alone.
		"config.yml:3": site("generic-api-key", ""),
		// `clé_api: "…"` — the cut lands after a two-byte rune and keeps it
		// whole.
		"config.yml:4": site("generic-api-key", "clé"),
	}
	for _, f := range got {
		key := f.Path + ":" + strconv.Itoa(f.Line)
		if f.Site != want[key] {
			t.Errorf("site for %s = %q, want %q", key, f.Site, want[key])
		}
	}
}

func TestOrdinalSeparatesTwoClaimsSharingASite(t *testing.T) {
	// Two secrets under one rule with no distinguishing prefix in one file are
	// ordinals 0 and 1 — the cost ADR 0035 names and accepts. Without the
	// ordinal they are one claim and the second is dropped as a duplicate.
	dir, rep := captured(t, "gitleaks")
	got, _ := findings(dir, rep)
	var sharing []finding.Finding
	for _, f := range got {
		if f.Path == "app.py" && f.Site == site("generic-api-key", "") {
			sharing = append(sharing, f)
		}
	}
	if len(sharing) != 2 {
		t.Fatalf("got %d claims sharing a site, want the capture's 2", len(sharing))
	}
	if sharing[0].Ordinal != 0 || sharing[1].Ordinal != 1 {
		t.Errorf("ordinals = %d and %d, want 0 and 1 in source order", sharing[0].Ordinal, sharing[1].Ordinal)
	}
	if sharing[0].Fingerprint() == sharing[1].Fingerprint() {
		t.Error("both claims share a fingerprint, so the second is dropped as a duplicate")
	}
}

func TestASiteSurvivesAnEditAboveIt(t *testing.T) {
	// The same tree with two lines inserted above every match, scanned by the
	// same gitleaks: every claim moves and none is re-identified.
	dir, rep := captured(t, "gitleaks")
	before, _ := findings(dir, rep)
	shiftedDir, shiftedRep := captured(t, "gitleaks-shifted")
	after, _ := findings(shiftedDir, shiftedRep)

	if len(before) != len(after) {
		t.Fatalf("got %d claims before the edit and %d after", len(before), len(after))
	}
	for i := range before {
		if before[i].Line == after[i].Line {
			t.Errorf("claim %d did not move: the fixture proves nothing", i)
		}
		if before[i].Fingerprint() != after[i].Fingerprint() {
			t.Errorf("claim %d at %s:%d was re-identified by an edit above it: site %q became %q",
				i, before[i].Path, before[i].Line, before[i].Site, after[i].Site)
		}
	}
}

func TestSourceOrderIsIndependentOfTheReportsOrder(t *testing.T) {
	// gitleaks scans files concurrently and its report's order varies between
	// runs. Ordinals are counted in the order given, so an unsorted parser hands
	// two claims sharing a site each other's identity from one run to the next.
	dir, rep := captured(t, "gitleaks")
	forwards, _ := findings(dir, rep)
	slices.Reverse(rep)
	backwards, _ := findings(dir, rep)

	for i := range forwards {
		if forwards[i].Line != backwards[i].Line || forwards[i].Path != backwards[i].Path {
			t.Fatalf("claim %d = %s:%d read forwards and %s:%d read backwards",
				i, forwards[i].Path, forwards[i].Line, backwards[i].Path, backwards[i].Line)
		}
		if forwards[i].Fingerprint() != backwards[i].Fingerprint() {
			t.Errorf("claim %d at %s:%d is identified differently depending on the report's order",
				i, forwards[i].Path, forwards[i].Line)
		}
	}
}

func TestALeakThatNamesNoLineIsReportedRatherThanDropped(t *testing.T) {
	// A parser that silently discards input is how a gate quietly stops working,
	// and the discarded thing here is a credential somebody committed.
	cases := []struct {
		name string
		leak leak
	}{
		{"no file", leak{RuleID: "generic-api-key", StartLine: 3}},
		{"line zero", leak{RuleID: "generic-api-key", File: "config.yml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unplaced := findings(t.TempDir(), report{tc.leak})
			if len(got) != 0 {
				t.Errorf("got %d claims, want none — neither a thread nor an identity can be made from this", len(got))
			}
			if len(unplaced) != 1 {
				t.Fatalf("unplaced = %q, want the leak named", unplaced)
			}
			if !strings.Contains(unplaced[0], "generic-api-key") {
				t.Errorf("unplaced = %q, want the rule named", unplaced[0])
			}
		})
	}
}

func TestAClaimOnTheFirstLineIsKept(t *testing.T) {
	// Line 1 is a real line; a boundary excluding it would drop every claim on
	// the first line of every file.
	dir, _ := captured(t, "gitleaks")
	got, unplaced := findings(dir, report{{RuleID: "r", File: "config.yml", StartLine: 1, StartColumn: 2}})
	if len(got) != 1 || len(unplaced) != 0 {
		t.Fatalf("got %d claims and %d unplaced, want 1 and 0", len(got), len(unplaced))
	}
}

func TestAPathIsNamedFromTheScanRoot(t *testing.T) {
	// gitleaks reports `./config.yml` for some inputs and `config.yml` for
	// others. One file named two ways is two claims, and only one of them can be
	// anchored.
	dir, _ := captured(t, "gitleaks")
	got, _ := findings(dir, report{{RuleID: "r", File: "./config.yml", StartLine: 3, StartColumn: 2}})
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if got[0].Path != "config.yml" {
		t.Errorf("path = %q, want it named from the scan root", got[0].Path)
	}
}

func TestASpanIsKeptOnlyWhenItIsOne(t *testing.T) {
	dir, _ := captured(t, "gitleaks")
	cases := []struct {
		name             string
		start, end, want int
	}{
		{"end equals start is one line", 3, 3, 0},
		{"no end stated", 3, 0, 0},
		{"a real span is kept", 3, 7, 7},
		{"an end before the start is not a span", 7, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := findings(dir, report{{RuleID: "r", File: "config.yml", StartLine: tc.start, EndLine: tc.end, StartColumn: 2}})
			if len(got) != 1 {
				t.Fatalf("got %d claims, want 1", len(got))
			}
			if got[0].EndLine != tc.want {
				t.Errorf("EndLine = %d, want %d", got[0].EndLine, tc.want)
			}
		})
	}
}

func TestPrefixEndStopsBeforeTheMatchAndOnARuneBoundary(t *testing.T) {
	// gitleaks reports a start column one byte past the match's first byte, so a
	// match beginning its line reads 2 and yields no prefix at all. Being before
	// the match is the whole of what keeps the secret out of the site.
	const line = `clé_api: "x"` // c l é(two bytes) _ a p i …
	cases := []struct {
		name string
		line string
		col  int
		want int
	}{
		{"a match beginning the line", line, 2, 0},
		{"a column below anything gitleaks reports", line, 1, 0},
		{"a column of zero", line, 0, 0},
		{"a negative column", line, -5, 0},
		{"one byte in", line, 3, 1},
		{"the cut landing after a two-byte rune", line, 6, 4},
		{"the cut landing inside a two-byte rune", line, 5, 2},
		{"the cut at the line's end", line, len(line) + 2, len(line)},
		{"a column past the line's end", line, len(line) + 99, len(line)},
		{"an empty line", "", 40, 0},
		// A file lydite does not own need not hold valid UTF-8, and a line
		// beginning with a continuation byte has no rune boundary to walk back
		// to. The cut is the line's start, not an index before it.
		{"a line that begins mid-rune", "\x80key = \"x\"", 2, 0},
		{"a cut walking back to the line's start", "\x80\x80\x80key", 4, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixEnd(tc.line, tc.col)
			if got != tc.want {
				t.Fatalf("prefixEnd(%q, %d) = %d, want %d", tc.line, tc.col, got, tc.want)
			}
			// Whatever the column says, the cut is a whole prefix of the line.
			if strings.ContainsRune(tc.line[:got], '�') {
				t.Errorf("prefixEnd(%q, %d) = %d, which splits a rune", tc.line, tc.col, got)
			}
		})
	}
}

func TestAPrefixIsEmptyForALineItCannotRead(t *testing.T) {
	// Empty is the right answer rather than an error: the caller is building an
	// identity, and the gate has already reported what it found.
	dir, _ := captured(t, "gitleaks")
	src := newTree(dir)
	cases := []struct {
		name string
		path string
		line int
	}{
		{"a file that is not there", "nothing.yml", 1},
		{"a path climbing out of the tree", "../outside.yml", 1},
		{"a line past the end", "config.yml", 9999},
		{"line zero", "config.yml", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := src.prefix(tc.path, tc.line, 40); got != "" {
				t.Errorf("prefix = %q, want empty", got)
			}
		})
	}
	// The line that is there still reads, so the cases above prove something.
	if got := src.prefix("config.yml", 4, 6); got != "clé" {
		t.Errorf("prefix = %q, want clé", got)
	}
	// A root that cannot be opened answers empty for everything rather than
	// failing the run.
	if got := newTree(filepath.Join(dir, "not-a-directory")).prefix("config.yml", 4, 6); got != "" {
		t.Errorf("prefix = %q, want empty from a tree that could not be opened", got)
	}
}

func TestAPrefixIsBoundedAndNormalised(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("é", maxSiteRunes+50) + `key="secret"`
	write(t, dir, "bundle.min.js", "  spaced \t out  key\n"+long+"\n")
	src := newTree(dir)

	// Reindenting a line must not re-identify the claim on it.
	if got := src.prefix("bundle.min.js", 1, 999); got != "spaced out key" {
		t.Errorf("prefix = %q, want the line's fields joined by single spaces", got)
	}
	// A minified bundle is one line of megabytes, and a site is uploaded in the
	// report document.
	got := src.prefix("bundle.min.js", 2, 4*maxSiteRunes)
	if n := len([]rune(got)); n != maxSiteRunes {
		t.Errorf("prefix is %d runes, want it clipped to %d", n, maxSiteRunes)
	}
}

func TestAFileOverTheReadCapIdentifiesNothing(t *testing.T) {
	// A file read part-way is a Site identifying a claim by text the file does
	// not hold, so an oversized one yields no prefix rather than a truncated
	// one.
	dir := t.TempDir()
	line := "prefix_key = \"x\"\n"
	write(t, dir, "at-the-cap.txt", line+strings.Repeat("f", maxFileBytes-len(line)))
	write(t, dir, "over-the-cap.txt", line+strings.Repeat("f", maxFileBytes-len(line)+1))
	src := newTree(dir)
	if got := src.prefix("at-the-cap.txt", 1, 8); got != "prefix" {
		t.Errorf("prefix = %q, want a file exactly at the cap read whole", got)
	}
	if got := src.prefix("over-the-cap.txt", 1, 8); got != "" {
		t.Errorf("prefix = %q, want none from a file over the cap", got)
	}
}

func TestAReportThatWillNotParseLeavesTheVerdictToTheExitStatus(t *testing.T) {
	for _, data := range []string{"", "not json at all", `[{"RuleID": `, `{"leaks":[]}`} {
		if _, ok := parseReport([]byte(data)); ok {
			t.Errorf("parseReport(%q) reported success", data)
		}
	}
	if _, ok := parseReport([]byte(`[]`)); !ok {
		t.Error("a report that does parse was rejected")
	}
}

func TestResultFallsBackToTheExitStatus(t *testing.T) {
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "no-report.json")
	corrupt := filepath.Join(dir, "corrupt.json")
	write(t, dir, "corrupt.json", `[{"RuleID": "generic-api-key"`)

	leaked := errors.New("exit status 1")
	cases := []struct {
		name   string
		path   string
		in     executil.Result
		wantOk bool
	}{
		{"no report beside a failing run", unreadable, executil.Result{Name: Gate, Err: leaked}, false},
		{"no report beside a clean run", unreadable, executil.Result{Name: Gate}, true},
		{"a corrupt report beside a failing run", corrupt, executil.Result{Name: Gate, Err: leaked}, false},
		{"a corrupt report beside a clean run", corrupt, executil.Result{Name: Gate}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := result(tc.in, dir, tc.path)
			if got.Ok() != tc.wantOk {
				t.Errorf("Ok() = %v, want %v — the exit status decides the row", got.Ok(), tc.wantOk)
			}
			if len(got.Findings) != 0 {
				t.Errorf("Findings = %d, want none from a report nothing could read", len(got.Findings))
			}
			if got.Detail != "" {
				t.Errorf("Detail = %q, want none: a parse failure is not a claim about the code", got.Detail)
			}
		})
	}
}

func TestResultReadsTheReportBesideTheExitStatus(t *testing.T) {
	dir, rep := captured(t, "gitleaks")
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshalling the capture: %v", err)
	}
	write(t, dir, "report.json", string(data))

	got := result(executil.Result{Name: Gate, Err: errors.New("exit status 1")}, dir, filepath.Join(dir, "report.json"))
	if len(got.Findings) != 5 {
		t.Errorf("Findings = %d, want the capture's 5", len(got.Findings))
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want none — gitleaks prints its own findings", got.Detail)
	}
	if got.Ok() {
		t.Error("Ok() = true beside a non-zero exit")
	}
}

func TestACleanRunSaysNothingOfItsOwn(t *testing.T) {
	// gitleaks prints its own findings, so Detail carries only what lydite has
	// to say — and over a clean tree that is nothing. A row that failed here
	// would fail every scan of a repository with no secrets in it.
	dir := t.TempDir()
	write(t, dir, "report.json", `[]`)
	got := result(executil.Result{Name: Gate}, dir, filepath.Join(dir, "report.json"))
	if !got.Ok() {
		t.Errorf("Ok() = false on a clean run: %v", got.Err)
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want none", got.Detail)
	}
	if len(got.Findings) != 0 {
		t.Errorf("Findings = %d, want none", len(got.Findings))
	}
}

func TestResultSaysWhatItCouldNotLocate(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "report.json", `[{"RuleID":"generic-api-key","StartLine":3}]`)
	path := filepath.Join(dir, "report.json")

	clean := result(executil.Result{Name: Gate}, dir, path)
	if clean.Ok() {
		t.Error("a leak reported beside a clean exit read as a pass")
	}
	if !strings.Contains(clean.Detail, "generic-api-key") {
		t.Errorf("Detail = %q, want the leak lydite could not place", clean.Detail)
	}

	leaked := errors.New("exit status 1")
	failing := result(executil.Result{Name: Gate, Err: leaked}, dir, path)
	if !errors.Is(failing.Err, leaked) {
		t.Errorf("Err = %v, want gitleaks' own failure kept", failing.Err)
	}
	if !strings.Contains(failing.Detail, "generic-api-key") {
		t.Errorf("Detail = %q, want the unplaced leak said whatever the status", failing.Detail)
	}
}

// secretsIn is every credential-shaped string in a probe file, so a test can
// assert that no site quotes one.
func secretsIn(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a path this test just materialised
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []string
	for _, field := range strings.FieldsFunc(string(data), func(r rune) bool { return !isHex(r) }) {
		if len(field) >= 32 {
			out = append(out, field)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no credential-shaped string, so this test proves nothing", path)
	}
	return out
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}
