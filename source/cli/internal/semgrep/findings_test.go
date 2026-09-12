package semgrep

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"lydite/lydite/internal/fixture"
)

// scannedDir is the tree the captured report names, materialised.
func scannedDir(t *testing.T) string {
	t.Helper()
	return fixture.Tree(t, "testdata/sgprobe")
}

// semgrepFixture is Semgrep's real --json-output report, captured from the
// pinned version.
func semgrepFixture(t *testing.T) report {
	t.Helper()
	data, err := os.ReadFile("testdata/semgrep.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return rep
}

func TestSemgrepReadsTheRealReport(t *testing.T) {
	got := findings(scannedDir(t), semgrepFixture(t))
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	f := got[0]
	if f.Gate != "semgrep" {
		t.Errorf("gate = %q, want semgrep", f.Gate)
	}
	if f.Path != "bad.py" {
		t.Errorf("path = %q, want bad.py", f.Path)
	}
	if f.Line != 3 {
		t.Errorf("line = %d, want 3", f.Line)
	}
	if f.Rule != "shell-true" {
		t.Errorf("rule = %q, want shell-true", f.Rule)
	}
	if f.Severity != "error" {
		t.Errorf("severity = %q, want error — Semgrep's own word, lower-cased", f.Severity)
	}
	if !strings.Contains(f.Message, "shell=True") {
		t.Errorf("message = %q, want Semgrep's own wording", f.Message)
	}
}

func TestSemgrepSiteIsReadFromTheTreeAndNotFromTheReport(t *testing.T) {
	// Semgrep's own extra.lines reads "requires login" on a logged-out run,
	// so a site built from it would identify every claim in the repository
	// alike — one fingerprint for the lot, and all but the first dropped.
	got := findings(scannedDir(t), semgrepFixture(t))
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if strings.Contains(got[0].Site, "requires login") {
		t.Errorf("site = %q, want the source line rather than Semgrep's placeholder", got[0].Site)
	}
	if !strings.Contains(got[0].Site, "subprocess.call") {
		t.Errorf("site = %q, want the text the rule fired on", got[0].Site)
	}
}

func TestSemgrepDoesNotReadAScanItCouldNotRunAsACleanPass(t *testing.T) {
	// Semgrep exits zero for a run whose rules would not load or whose files
	// would not parse, so a scan that read nothing reports no findings and
	// passes. This is the failure reportableBiome's `parse` and
	// `internalError/io` categories exist to catch.
	rep := report{Errors: []reportError{{Level: "error", Message: "invalid rule schema", Path: "r.yaml"}}}
	got := blocking(rep)
	if len(got) != 1 {
		t.Fatalf("blocking = %q, want the one error", got)
	}
	if !strings.Contains(got[0], "r.yaml") {
		t.Errorf("blocking = %q, want the file named", got)
	}
}

func TestSemgrepCountsAnErrorLevelItDoesNotRecognise(t *testing.T) {
	// The levels are open-ended and every one missed is silent, so anything
	// that is not explicitly a warning counts.
	rep := report{Errors: []reportError{{Level: "a-level-from-a-later-semgrep", Message: "something new"}}}
	if got := blocking(rep); len(got) != 1 {
		t.Errorf("blocking = %q, want the unrecognised level counted", got)
	}
}

func TestSemgrepDoesNotBlockOnAWarning(t *testing.T) {
	rep := report{Errors: []reportError{{Level: "warn", Message: "a skipped file"}}}
	if got := blocking(rep); len(got) != 0 {
		t.Errorf("blocking = %q, want none — a warning is not a scan that failed to run", got)
	}
}

func TestSemgrepSkipsAResultThatLocatesNothing(t *testing.T) {
	rep := report{Results: []result{{CheckID: "r", Path: "", Start: struct {
		Line int `json:"line"`
		Col  int `json:"col"`
	}{Line: 0}}}}
	if got := findings(t.TempDir(), rep); len(got) != 0 {
		t.Fatalf("got %d claims, want none", len(got))
	}
}

func TestSemgrepNumbersTwoClaimsOfOneRuleOnOneLine(t *testing.T) {
	// Two rules of one id firing on one line are told apart by their ordinal,
	// which is what keeps them two claims rather than one reported once.
	line := struct {
		Line int `json:"line"`
		Col  int `json:"col"`
	}{Line: 3}
	rep := report{Results: []result{
		{CheckID: "shell-true", Path: "bad.py", Start: line},
		{CheckID: "shell-true", Path: "bad.py", Start: line},
	}}
	got := findings(scannedDir(t), rep)
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Error("both claims share a fingerprint, so the second is dropped as a duplicate")
	}
}

func TestSemgrepFallsBackToTheExitStatusOnAnUnreadableReport(t *testing.T) {
	for _, data := range []string{"", "not json at all", `{"results": `} {
		if _, ok := parseReport([]byte(data)); ok {
			t.Errorf("parseReport(%q) reported success", data)
		}
	}
	if _, ok := parseReport([]byte(`{"results":[]}`)); !ok {
		t.Error("a report that does parse was rejected")
	}
}

func TestReportArgsWritesACopy(t *testing.T) {
	// --json-output writes a copy: Semgrep keeps printing its findings and
	// keeps its exit status. --json would replace the output entirely, leaving
	// a developer with a failing row and no finding named anywhere.
	base := []string{"scan", "--config", "auto", "--error"}
	got := reportArgs(base, "/tmp/report.json")
	if got[len(got)-1] != "--json-output=/tmp/report.json" {
		t.Errorf("argv = %q, want the report flag appended", got)
	}
	for _, arg := range got {
		if arg == "--json" {
			t.Errorf("argv = %q, want --json-output rather than --json", got)
		}
	}
	// The caller's own arguments are not written through: buildArgs' slice is
	// reused across both of Check's branches.
	if len(base) != 4 {
		t.Errorf("the caller's args were modified: %q", base)
	}
}

func TestSemgrepKeepsAResultOnTheFirstLine(t *testing.T) {
	// Line 1 is a real line; a boundary that excluded it would drop every
	// finding on the first line of every file. Line 0 is not, and a claim
	// carrying it can be anchored to nothing.
	line := func(n int) result {
		var r result
		r.CheckID = "rule"
		r.Path = "bad.py"
		r.Start.Line = n
		return r
	}
	if got := findings(scannedDir(t), report{Results: []result{line(1)}}); len(got) != 1 {
		t.Errorf("got %d claims for line 1, want 1", len(got))
	}
	if got := findings(scannedDir(t), report{Results: []result{line(0)}}); len(got) != 0 {
		t.Errorf("got %d claims for line 0, want none", len(got))
	}
}

func TestSemgrepReportsNoSpanForASingleLineResult(t *testing.T) {
	// EndLine is zero for a claim about one line, so a consumer need not tell
	// a one-line span from a zero-length one. A result whose end equals its
	// start is one line.
	spanning := func(start, end int) result {
		var r result
		r.CheckID = "rule"
		r.Path = "bad.py"
		r.Start.Line = start
		r.End.Line = end
		return r
	}
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
			got := findings(scannedDir(t), report{Results: []result{spanning(tc.start, tc.end)}})
			if len(got) != 1 {
				t.Fatalf("got %d claims, want 1", len(got))
			}
			if got[0].EndLine != tc.want {
				t.Errorf("EndLine = %d, want %d", got[0].EndLine, tc.want)
			}
		})
	}
}
