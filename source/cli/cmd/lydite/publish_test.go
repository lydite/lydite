package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/ui"
)

// reportDirWith writes a report directory holding one document, the way a run
// leaves it behind and a CI job uploads it.
func reportDirWith(t *testing.T, command string, rows ...ui.Row) string {
	t.Helper()
	root := t.TempDir()
	rep := ui.NewReport(command)
	for _, row := range rows {
		rep.Add(row)
	}
	saveDocument(root, rep)
	return reportsDir(root)
}

// A named directory that is not there must be named in the comment. A section
// that quietly disappears reads exactly like a concern that passed, which is
// how a pull request goes green while the gate that would have failed it never
// ran.
func TestAnAbsentReportDirectoryIsReportedAndNotOmitted(t *testing.T) {
	present := reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})
	comment := buildComment([]string{present, filepath.Join(t.TempDir(), "never-uploaded")}, "")

	body := comment.Render()
	if !strings.Contains(body, "never-uploaded") {
		t.Fatalf("the missing directory is not named:\n%s", body)
	}
	if !strings.Contains(body, "no such directory") {
		t.Errorf("the comment does not say what was wrong:\n%s", body)
	}
	if comment.Verdict != ui.VerdictPass {
		t.Errorf("verdict is %q; a missing input is unmeasured and votes on nothing", comment.Verdict)
	}
	if !strings.Contains(comment.Headline, "no verdict came from") {
		t.Errorf("the headline reads as a clean run: %q", comment.Headline)
	}
}

// A directory that exists but holds no document is the same failure wearing a
// different hat: the job ran, uploaded something, and produced no verdict.
func TestADirectoryHoldingNoDocumentIsReported(t *testing.T) {
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "test.log"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildComment([]string{empty}, "").Render()
	if !strings.Contains(body, "holds no report document") {
		t.Fatalf("an empty report directory was not reported:\n%s", body)
	}
}

// A row naming a log that was not uploaded still has to render. The tail is a
// convenience; losing it must not lose the row.
func TestARowWhoseLogIsMissingStillRenders(t *testing.T) {
	dir := reportDirWith(t, "scan", ui.Row{
		Status: ui.StatusFail, Label: "gosec(cli)", Value: "failed",
		Log: ".lydite-reports/scan/gosec-cli-.log",
	})
	body := buildComment([]string{dir}, "").Render()
	if !strings.Contains(body, "gosec(cli)") {
		t.Fatalf("the failing row was dropped with its log:\n%s", body)
	}
	if strings.Contains(body, "```\n```") {
		t.Errorf("an empty fenced block was rendered for the absent log:\n%s", body)
	}
}

// A failing row that streamed its findings carries no Detail, so the log is
// the only place the reason exists — which is the whole reason scan writes one.
func TestAFailingRowFallsBackToItsLogForTheReason(t *testing.T) {
	root := t.TempDir()
	rel := checkLog(root, "gosec(cli)", "G306: Expect WriteFile permissions to be 0600 or less\n")
	if rel == "" {
		t.Fatal("no log was written")
	}
	rep := ui.NewReport("scan")
	rep.Add(ui.Row{Status: ui.StatusFail, Label: "gosec(cli)", Value: "failed", Log: rel})
	saveDocument(root, rep)

	body := buildComment([]string{reportsDir(root)}, "").Render()
	if !strings.Contains(body, "G306") {
		t.Fatalf("the log's tail did not reach the comment:\n%s", body)
	}
}

// Detail is what the row's author chose to put next to the verdict — Biome's
// findings reach a reader no other way — so it wins over the log.
func TestDetailIsPreferredOverTheLog(t *testing.T) {
	root := t.TempDir()
	rel := checkLog(root, "biome(web)", "chatter nobody wants\n")
	rep := ui.NewReport("scan")
	rep.Add(ui.Row{
		Status: ui.StatusFail, Label: "biome(web)", Value: "failed",
		Detail: []string{"src/bad.ts:1  lint/security/noGlobalEval"},
		Log:    rel,
	})
	saveDocument(root, rep)

	body := buildComment([]string{reportsDir(root)}, "").Render()
	if !strings.Contains(body, "noGlobalEval") {
		t.Fatalf("the detail did not reach the comment:\n%s", body)
	}
	if strings.Contains(body, "chatter nobody wants") {
		t.Error("the log was rendered as well as the detail")
	}
}

// Every concern a run reported appears, in a declared order, so two pull
// requests never present the same results differently.
func TestSectionsAreInADeclaredOrder(t *testing.T) {
	dirs := []string{
		reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"}),
		reportDirWith(t, "review", ui.Row{Status: ui.StatusRefer, Label: "referral", Value: "no exemption matched"}),
		reportDirWith(t, "scan", ui.Row{Status: ui.StatusPass, Label: "gosec(cli)", Value: "passed"}),
	}
	comment := buildComment(dirs, "")
	var titles []string
	for _, s := range comment.Sections {
		titles = append(titles, s.Title)
	}
	want := []string{"referral", "scan", "test"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Fatalf("sections are %v, want %v", titles, want)
	}
}

// A failure outranks a referral because it is actionable by the author, which
// is the report's own precedence asked of a whole run.
func TestAFailureOutranksAReferralAcrossSections(t *testing.T) {
	dirs := []string{
		reportDirWith(t, "review", ui.Row{Status: ui.StatusRefer, Label: "referral", Value: "no exemption matched"}),
		reportDirWith(t, "test", ui.Row{Status: ui.StatusFail, Label: "test(web)", Value: "failed"}),
	}
	comment := buildComment(dirs, "")
	if comment.Verdict != ui.VerdictFail {
		t.Fatalf("verdict is %q, want fail", comment.Verdict)
	}
	if !strings.Contains(comment.Headline, "test") {
		t.Errorf("the headline does not name the concern to act on: %q", comment.Headline)
	}
}

// A referral is resolved by a person, and the comment is where they are told
// how.
func TestAReferralSaysWhatResolvesIt(t *testing.T) {
	dir := reportDirWith(t, "review", ui.Row{Status: ui.StatusRefer, Label: "referral", Value: "no exemption matched"})
	comment := buildComment([]string{dir}, "")
	if comment.Verdict != ui.VerdictRefer {
		t.Fatalf("verdict is %q, want refer", comment.Verdict)
	}
	if !strings.Contains(comment.Headline, "/lydite clear") {
		t.Errorf("the headline does not say what resolves it: %q", comment.Headline)
	}
}

// One scan root produces every command's document in one directory, which is
// what a developer running the three locally has.
func TestOneDirectoryCanHoldEveryCommandsDocument(t *testing.T) {
	root := t.TempDir()
	for _, command := range []string{"scan", "test", "review"} {
		rep := ui.NewReport(command)
		rep.Add(ui.Row{Status: ui.StatusPass, Label: command + "(cli)", Value: "passed"})
		saveDocument(root, rep)
	}
	comment := buildComment([]string{reportsDir(root)}, "")
	if len(comment.Sections) != 3 {
		t.Fatalf("%d sections, want 3", len(comment.Sections))
	}
}

// A label is built for a reader — a space in `cargo clippy`, parentheses
// around the component — and none of that may reach a filename.
func TestALogNameIsAFilenameOnEveryPlatform(t *testing.T) {
	for label, want := range map[string]string{
		"cargo clippy(cli)": "cargo-clippy-cli.log",
		"gosec(cli)":        "gosec-cli.log",
		"a/b":               "a-b.log",
		"...":               "check.log",
	} {
		if got := logName(label); got != want {
			t.Errorf("logName(%q) = %q, want %q", label, got, want)
		}
	}
	for _, label := range []string{"cargo clippy(cli)", "a/b", "..", ""} {
		got := logName(label)
		if strings.ContainsAny(got, `/\ ()`) {
			t.Errorf("logName(%q) = %q, which is not a filename", label, got)
		}
	}
}

// A row's log is relative to the scan root, and what a caller has is the
// report directory under whatever name the artifact was downloaded as.
func TestALogResolvesAgainstTheDirectoryItWasDownloadedInto(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lydite-reports-test")
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "test.log"), []byte("FAIL src/app.test.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readLog(dir, ".lydite-reports/web/test.log")
	if len(got) != 1 || got[0] != "FAIL src/app.test.ts" {
		t.Fatalf("the log did not resolve: %v", got)
	}
}

// A log path is lydite's own, but it arrives through a file a CI job
// downloaded, so a traversal out of the directory must not be followed.
func TestALogPathCannotEscapeTheReportDirectory(t *testing.T) {
	if got := readLog(t.TempDir(), ".lydite-reports/../../../etc/passwd"); got != nil {
		t.Fatalf("a traversal was followed: %v", got)
	}
}

// A hosting platform refuses a comment over its size limit, and a refused
// comment is no surface at all — which is the outcome this feature exists to
// prevent. Every failing row still appears in the table.
func TestQuotedOutputIsCappedButNoRowIsLost(t *testing.T) {
	rep := ui.NewReport("test")
	for i := range detailCap + 3 {
		rep.Add(ui.Row{
			Status: ui.StatusFail,
			Label:  fmt.Sprintf("test(c%d)", i),
			Value:  "failed",
			Detail: []string{fmt.Sprintf("component %d blew up", i)},
		})
	}
	root := t.TempDir()
	saveDocument(root, rep)

	comment := buildComment([]string{reportsDir(root)}, "")
	body := comment.Render()
	if n := len(comment.Sections[0].Details); n != detailCap {
		t.Errorf("%d blocks of output were quoted, want %d", n, detailCap)
	}
	for i := range detailCap + 3 {
		if !strings.Contains(body, fmt.Sprintf("test(c%d)", i)) {
			t.Errorf("row for component %d is missing from the table:\n%s", i, body)
		}
	}
	if !strings.Contains(body, "3 further failure(s)") {
		t.Errorf("the comment does not say how many failures it left out:\n%s", body)
	}
}
