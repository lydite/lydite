package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
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

// A job that dies before it uploads leaves no artifact, so the directory it
// would have been downloaded into is never there to be named — the concern
// reaches the comment as nothing at all rather than as something unreadable.
// The run says which concerns it expected, and one that arrived in no
// directory is a section rather than a silence.
func TestAConcernExpectedFromNoDirectoryAtAllIsNamed(t *testing.T) {
	present := reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})
	comment := buildComment([]string{present}, "", "review", "test")

	body := comment.Render()
	if !strings.Contains(body, "referral") {
		t.Fatalf("the concern that produced no report is absent from the comment:\n%s", body)
	}
	if !strings.Contains(body, "the run expected a `review` report") {
		t.Errorf("the comment does not say what was missing:\n%s", body)
	}
	if comment.Headline == "every check passed" {
		t.Errorf("the headline reads as a clean run: %q", comment.Headline)
	}
	if !strings.Contains(comment.Headline, "no verdict came from referral") {
		t.Errorf("the headline does not name the concern that went unmeasured: %q", comment.Headline)
	}
}

// A concern nobody expected is still considered only if a document for it
// arrives. A run that legitimately does not run a command must not be told it
// is missing one.
func TestAnUnexpectedConcernIsNotReportedMissing(t *testing.T) {
	present := reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})
	comment := buildComment([]string{present}, "", "test")

	if got := comment.Headline; got != "every check passed" {
		t.Errorf("headline is %q; nothing was missing that the run asked for", got)
	}
	if body := comment.Render(); strings.Contains(body, "referral") {
		t.Errorf("a concern the run never expected is reported as missing:\n%s", body)
	}
}

// A concern expected under a name this binary does not declare in the
// concerns table still has to be named, not silently folded away for want of
// a title lydite recognizes.
func TestAnExpectedUndeclaredConcernIsNamed(t *testing.T) {
	present := reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})
	comment := buildComment([]string{present}, "", "test", "coverage")

	body := comment.Render()
	if !strings.Contains(body, "coverage") {
		t.Fatalf("the undeclared concern is absent from the comment:\n%s", body)
	}
	if !strings.Contains(body, "the run expected a `coverage` report") {
		t.Errorf("the comment does not say what was missing:\n%s", body)
	}
	if comment.Headline == "every check passed" {
		t.Errorf("the headline reads as a clean run: %q", comment.Headline)
	}
}

// An undeclared concern that does arrive is rendered from what was found, not
// also reported as missing because its name is absent from the concerns
// table.
func TestAnExpectedUndeclaredConcernThatArrivesIsNotAlsoReportedMissing(t *testing.T) {
	dir := reportDirWith(t, "coverage", ui.Row{Status: ui.StatusPass, Label: "coverage(cli)", Value: "passed"})
	comment := buildComment([]string{dir}, "", "coverage")

	if got := comment.Headline; got != "every check passed" {
		t.Errorf("headline is %q; the expected concern arrived", got)
	}
	if body := comment.Render(); strings.Contains(body, "the run expected a `coverage` report") {
		t.Errorf("an expected concern that arrived is also reported missing:\n%s", body)
	}
}

// --expect is a flag on the command, not only a parameter buildComment
// happens to take — a run invoking `lydite publish --expect ...` has to reach
// the same behaviour the unit tests exercise directly.
func TestTheExpectFlagIsWiredToTheRun(t *testing.T) {
	present := reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})

	cmd := newPublishCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--reports", present, "--expect", "review,test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("publish: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "the run expected a `review` report") {
		t.Fatalf("--expect did not reach the run:\n%s", out.String())
	}
}

// sortedNames is what keeps an undeclared concern's place in the comment
// stable across runs, rather than following Go's randomised map order.
func TestSortedNamesOrdersAlphabetically(t *testing.T) {
	got := sortedNames(map[string]bool{
		"zeta": true, "alpha": true, "mid": true, "kappa": true, "omega": true, "beta": true,
	})
	want := []string{"alpha", "beta", "kappa", "mid", "omega", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortedNames(...) = %v, want %v", got, want)
	}
}

// A blank --expect entry names nothing, and must not itself render as a
// concern the run expected.
func TestABlankExpectEntryIsIgnored(t *testing.T) {
	present := reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})
	comment := buildComment([]string{present}, "", "test", "", "  ")

	if got := comment.Headline; got != "every check passed" {
		t.Errorf("headline is %q; a blank --expect entry names no concern", got)
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

// A referral's value is the verdict and its detail is what about this change
// produced it. Without the detail the section reads `referral … no exemptions
// declared` and nothing else, which a reader takes for a misconfiguration
// rather than for the answer — and the one actionable line, how to resolve it,
// is the line that goes missing.
func TestAReferralExplainsItselfInTheComment(t *testing.T) {
	root := t.TempDir()
	rep := ui.NewReport("review")
	rep.Add(ui.Row{
		Status: ui.StatusRefer, Label: "referral", Value: "no exemptions declared",
		Detail: []string{
			".lydite/exemptions.yml declares no exemptions, so every change is referred",
			"ask a human to clear this change",
		},
	})
	saveDocument(root, rep)

	body := buildComment([]string{reportsDir(root)}, "").Render()
	for _, want := range []string{"declares no exemptions", "ask a human to clear this change"} {
		if !strings.Contains(body, want) {
			t.Errorf("the comment does not carry %q:\n%s", want, body)
		}
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
	if !strings.Contains(body, "3 further result(s)") {
		t.Errorf("the comment does not say how many failures it left out:\n%s", body)
	}
}

// A run that measured coverage without gating it renders every coverage row as
// context. A summary counting only the rows that vote would describe that run
// and a fully gated one identically, which is the distinction ADR 0019 exists
// to keep.
func TestTheSummaryAccountsForRowsThatVoteOnNothing(t *testing.T) {
	dir := reportDirWith(t, "test",
		ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"},
		ui.Row{Status: ui.StatusContext, Label: "coverage(cli)", Value: "81.5%"},
		ui.Row{Status: ui.StatusContext, Label: "coverage(repo)", Value: "81.5%"},
		ui.Row{Status: ui.StatusNew, Label: "coverage(web)", Value: "no baseline yet"},
	)
	got := buildComment([]string{dir}, "").Sections[0].Summary
	for _, want := range []string{"1 passed", "1 new", "2 not gated"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q is missing %q", got, want)
		}
	}
}

// A section made entirely of declined rows is a decision stated on purpose,
// not a gap — it must not read as unmeasured, and the run that produced it
// must not be told "no verdict came from" the concern it chose to skip.
func TestADeclinedSectionIsNotUnmeasured(t *testing.T) {
	dir := reportDirWith(t, "mutation",
		ui.Row{Status: ui.StatusDeclined, Label: "mutation", Value: "declined for this run"},
	)
	comment := buildComment([]string{dir}, "")
	if got := comment.Sections[0].Status; got != ui.StatusDeclined {
		t.Fatalf("section status is %q, want declined", got)
	}
	if comment.Verdict != ui.VerdictPass {
		t.Fatalf("verdict is %q, want pass", comment.Verdict)
	}
	if strings.Contains(comment.Headline, "no verdict came from") {
		t.Errorf("a declined concern must not read as one that went missing: %q", comment.Headline)
	}
	if comment.Headline != "every check passed" {
		t.Errorf("headline is %q, want %q", comment.Headline, "every check passed")
	}
}

// A section that fails alongside a declined row must still fail — declined
// never outranks a real failure.
func TestAFailureOutranksADeclinedRowInTheSameSection(t *testing.T) {
	dir := reportDirWith(t, "mutation",
		ui.Row{Status: ui.StatusFail, Label: "mutation(cli)", Value: "failed"},
		ui.Row{Status: ui.StatusDeclined, Label: "mutation(web)", Value: "declined for this run"},
	)
	comment := buildComment([]string{dir}, "")
	if got := comment.Sections[0].Status; got != ui.StatusFail {
		t.Fatalf("section status is %q, want fail", got)
	}
	if comment.Verdict != ui.VerdictFail {
		t.Fatalf("verdict is %q, want fail", comment.Verdict)
	}
}

// The shut summary names a declined concern by its own word, distinct from
// "unmeasured", so a reader does not have to open the section to tell a gap
// from a decision.
func TestTheSummaryNamesADeclinedRow(t *testing.T) {
	dir := reportDirWith(t, "mutation",
		ui.Row{Status: ui.StatusDeclined, Label: "mutation", Value: "declined for this run"},
	)
	got := buildComment([]string{dir}, "").Sections[0].Summary
	if !strings.Contains(got, "1 declined") {
		t.Errorf("summary %q is missing %q", got, "1 declined")
	}
}

// StatusDeclined is non-voting, like StatusContext — it must never turn a
// comment's badge into a referral or a failure on its own.
func TestADeclinedSectionDoesNotVoteOnTheVerdict(t *testing.T) {
	dir := reportDirWith(t, "mutation",
		ui.Row{Status: ui.StatusDeclined, Label: "mutation", Value: "declined for this run"},
	)
	comment := buildComment([]string{dir}, "")
	if verdictOf(comment.Sections) != ui.VerdictPass {
		t.Fatalf("verdictOf = %q, want pass", verdictOf(comment.Sections))
	}
}

// Mutation is the fourth concern, and it reaches the comment through the
// mechanism the other three already use: a document a run wrote, rendered by
// the generic mapping from a row's label and value. The order is declared
// rather than derived from what a run happened to produce, so two pull
// requests never present the same concerns in a different order.
func TestTheCommentCarriesMutationAfterTheOtherThreeConcerns(t *testing.T) {
	dirs := []string{
		reportDirWith(t, "mutation", ui.Row{Status: ui.StatusFail, Label: "mutation(cli)", Value: "1 of 9 mutant(s) survived in 2m4s"}),
		reportDirWith(t, "test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"}),
		reportDirWith(t, "scan", ui.Row{Status: ui.StatusPass, Label: "gosec(cli)", Value: "clean"}),
		reportDirWith(t, "review", ui.Row{Status: ui.StatusPass, Label: "referral", Value: "exempt"}),
	}
	comment := buildComment(dirs, "")

	var titles []string
	for _, s := range comment.Sections {
		titles = append(titles, s.Title)
	}
	want := []string{"referral", "scan", "test", "mutation"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Fatalf("sections are %v, want %v", titles, want)
	}
	if comment.Verdict != ui.VerdictFail {
		t.Errorf("verdict is %q; a survivor fails the run", comment.Verdict)
	}
	body := comment.Render()
	if !strings.Contains(body, "1 of 9 mutant(s) survived") {
		t.Errorf("the survivor does not reach the comment:\n%s", body)
	}
}

// reportDirWithFindings writes a report directory whose document carries both
// rows and the located claims the run made.
func reportDirWithFindings(t *testing.T, command string, rows []ui.Row, found []finding.Finding) string {
	t.Helper()
	root := t.TempDir()
	rep := ui.NewReport(command)
	for _, row := range rows {
		rep.Add(row)
	}
	rep.AddFindings(found...)
	saveDocument(root, rep)
	return reportsDir(root)
}

// A claim that reaches a line of the change is a thread on that line, so the
// comment says how many there are rather than repeating them. Repeating one
// would put the same claim in two places, only one of which a reader can
// resolve.
func TestTheCommentLeavesLocatedFindingsToTheReview(t *testing.T) {
	row := ui.Row{Status: ui.StatusFail, Label: "mutation(cli)", Value: "2 of 8 mutant(s) survived",
		Detail: []string{"a survivor", "another survivor", "3 did not compile"}}
	dir := reportDirWithFindings(t, "mutation", []ui.Row{row}, []finding.Finding{
		{Gate: "mutation", Component: "cli", Path: "a.go", Line: 12, Message: "a survivor",
			Site: "one", Row: "mutation(cli)", Anchor: finding.AnchorLine},
		{Gate: "mutation", Component: "cli", Path: "b.go", Line: 40, Message: "an unreachable survivor",
			Site: "two", Row: "mutation(cli)"},
	})

	body := buildComment([]string{dir}, "").Render()
	if !strings.Contains(body, "an unreachable survivor") {
		t.Errorf("a claim that reaches the change nowhere must stay in the comment:\n%s", body)
	}
	if strings.Contains(body, "a survivor\n") {
		t.Errorf("a claim with a line of its own is repeated here:\n%s", body)
	}
	if !strings.Contains(body, "1 finding(s) reach a line of this change") {
		t.Errorf("the comment does not say what it is leaving out:\n%s", body)
	}
}

// The narrowing does not ask whether threads are being posted, so a developer
// running publish locally reads exactly the comment a reviewer sees — the
// parity ADR 0023 exists for.
func TestARowWhoseClaimsAreAllLocatedStillSaysSo(t *testing.T) {
	row := ui.Row{Status: ui.StatusFail, Label: "biome(cli)", Value: "failed", Detail: []string{"a finding"}}
	dir := reportDirWithFindings(t, "scan", []ui.Row{row}, []finding.Finding{
		{Gate: "biome", Component: "cli", Path: "a.ts", Line: 3, Message: "a finding",
			Site: "one", Row: "biome(cli)", Anchor: finding.AnchorLine},
	})
	body := buildComment([]string{dir}, "").Render()
	if !strings.Contains(body, "1 finding(s) reach a line of this change") {
		t.Fatalf("a row with every claim on a line reads as a row with nothing under it:\n%s", body)
	}
}

// A row whose every claim reaches the change nowhere says nothing about
// threads, because there are none: a count of zero is a sentence a reader has
// to read and discard.
func TestARowWithNoLocatedFindingsCountsNothing(t *testing.T) {
	row := ui.Row{Status: ui.StatusFail, Label: "crap(cli)", Value: "1 more"}
	dir := reportDirWithFindings(t, "test", []ui.Row{row}, []finding.Finding{
		{Gate: "crap", Component: "cli", Path: "a.go", Line: 40, Message: "a complex function",
			Site: "one", Row: "crap(cli)"},
	})
	body := buildComment([]string{dir}, "").Render()
	if !strings.Contains(body, "a complex function") {
		t.Fatalf("the claim is not in the comment:\n%s", body)
	}
	if strings.Contains(body, "reach a line of this change") {
		t.Fatalf("a count of nothing was rendered:\n%s", body)
	}
}

// Which rule fired is what a reader acts on, and a scanner's claims reach the
// change nowhere — so the comment is the only surface that ever carries them.
func TestAScannersClaimNamesTheRuleThatFired(t *testing.T) {
	row := ui.Row{Status: ui.StatusFail, Label: "biome(cli)", Value: "failed"}
	dir := reportDirWithFindings(t, "scan", []ui.Row{row}, []finding.Finding{
		{Gate: "biome", Component: "cli", Path: "a.ts", Line: 3, Rule: "lint/suspicious/noExplicitAny",
			Message: "avoid any", Site: "one", Row: "biome(cli)"},
	})
	body := buildComment([]string{dir}, "").Render()
	if !strings.Contains(body, "lint/suspicious/noExplicitAny") {
		t.Fatalf("the rule is not in the comment:\n%s", body)
	}
}

// A gate that emits no findings still quotes what its author put next to the
// verdict, which for several checks is the only place their output exists.
func TestARowWithNoFindingsQuotesItsOwnDetail(t *testing.T) {
	row := ui.Row{Status: ui.StatusFail, Label: "cargo clippy(engine)", Value: "failed",
		Detail: []string{"error: this is the only copy of this text"}}
	dir := reportDirWithFindings(t, "scan", []ui.Row{row}, nil)
	body := buildComment([]string{dir}, "").Render()
	if !strings.Contains(body, "the only copy of this text") {
		t.Fatalf("a row with no findings lost its detail:\n%s", body)
	}
}

func TestAClaimWithNoLineIsShownAsTheFileAlone(t *testing.T) {
	// A dependency advisory whose manifest line cannot be found carries line
	// zero deliberately: the lookup refuses to guess rather than point at code
	// the author cannot act on. Rendering `go.mod:0` would put that invented
	// reference straight back.
	unlocated := finding.Finding{
		Path: "go.mod", Line: 0, Rule: "GO-2020-0036", Message: "an advisory",
	}
	if got := claimLine(unlocated); strings.Contains(got, ":0") {
		t.Errorf("claimLine = %q, want the file with no line", got)
	} else if !strings.HasPrefix(got, "go.mod GO-2020-0036") {
		t.Errorf("claimLine = %q, want the file, the rule and the message", got)
	}

	located := unlocated
	located.Line = 7
	if got := claimLine(located); !strings.HasPrefix(got, "go.mod:7 ") {
		t.Errorf("claimLine = %q, want the line kept when there is one", got)
	}
}

func TestAFailingRowSClaimsAreBoundedInTheComment(t *testing.T) {
	// Every scanner emits findings, so one row over a repository with standing
	// debt lists hundreds. A comment over the platform's byte limit is refused
	// outright, and a section that vanishes reads as a concern that passed.
	var found []finding.Finding
	for i := range 200 {
		found = append(found, finding.Finding{
			Gate: "gosec", Path: "a.go", Line: i + 1, Rule: "G401",
			Message: strings.Repeat("long ", 400),
			Site:    fmt.Sprintf("site-%d", i),
		})
	}
	lines := detailFor(t.TempDir(), ui.Row{Status: ui.StatusFail, Label: "gosec(cli)"}, found)
	if len(lines) > tailLines+2 {
		t.Errorf("the row rendered %d lines, want it bounded near %d", len(lines), tailLines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "more finding(s) in this row") {
		t.Error("claims were dropped without saying so")
	}
	for _, line := range lines {
		if len([]rune(line)) > claimRunes+200 {
			t.Errorf("a claim ran to %d runes, want each bounded near %d", len([]rune(line)), claimRunes)
		}
	}
}

func TestAFailingRowSaysNothingAboutClaimsItDidNotDrop(t *testing.T) {
	// The overflow line is what tells a reader the quote stops short. A row
	// whose claims all fit must not carry it, or every comment says findings
	// were withheld when none were.
	found := []finding.Finding{
		{Gate: "gosec", Path: "a.go", Line: 1, Rule: "G401", Message: "a claim", Site: "one"},
	}
	lines := detailFor(t.TempDir(), ui.Row{Status: ui.StatusFail, Label: "gosec(cli)"}, found)
	for _, line := range lines {
		if strings.Contains(line, "more finding(s) in this row") {
			t.Errorf("lines = %q, want no overflow line when nothing overflowed", lines)
		}
	}
	// And exactly at the cap it still says nothing.
	var atCap []finding.Finding
	for i := range tailLines {
		atCap = append(atCap, finding.Finding{
			Gate: "gosec", Path: "a.go", Line: i + 1, Rule: "G401",
			Message: "a claim", Site: fmt.Sprintf("site-%d", i),
		})
	}
	lines = detailFor(t.TempDir(), ui.Row{Status: ui.StatusFail, Label: "gosec(cli)"}, atCap)
	if len(lines) != tailLines {
		t.Errorf("rendered %d lines for exactly the cap, want %d with no overflow line", len(lines), tailLines)
	}
}

func TestClipClaimAtExactlyTheCap(t *testing.T) {
	// A message at the cap is kept whole; one rune over is clipped. The
	// boundary is what the cap means.
	at := clipClaim(strings.Repeat("x", claimRunes))
	if len([]rune(at)) != claimRunes || strings.HasSuffix(at, "…") {
		t.Errorf("a message at the cap was clipped: %d runes", len([]rune(at)))
	}
	if over := clipClaim(strings.Repeat("x", claimRunes+1)); !strings.HasSuffix(over, "…") {
		t.Error("a message one rune over the cap was not clipped")
	}
}

// clippy, cargo-audit and cargo-deny each run once, in JSON mode, so the output
// scan writes to the row's log is the machine report and nothing a reader can
// use. A failing row of theirs carries its own Detail — the claims it parsed, or
// the reason a report that named none still failed — and that Detail is what the
// comment quotes. A row that arrived here with none would fall back to the log
// and paste the report into a public pull-request comment.
func TestAFailingCargoToolRowNeverQuotesItsRawJSONLog(t *testing.T) {
	cases := []struct {
		name   string
		result executil.Result
		want   string
	}{
		{
			name: "clippy claims",
			result: executil.Result{
				Name:   "cargo clippy(engine)",
				Output: `{"reason":"compiler-message","message":{"code":{"code":"clippy::needless_borrow"},"level":"warning","spans":[{"file_name":"src/lib.rs","line_start":12,"is_primary":true}],"rendered":"warning: this expression creates a reference which is immediately dereferenced"}}` + "\n",
				Detail: "src/lib.rs:12  clippy::needless_borrow  this expression creates a reference which is immediately dereferenced\n" +
					"  warning: this expression creates a reference which is immediately dereferenced\n",
				Err: errors.New("exit status 101"),
			},
			want: "clippy::needless_borrow",
		},
		{
			name: "an audit report naming no claim",
			result: executil.Result{
				Name:   "cargo audit(engine)",
				Output: `{"database":{"advisory-count":712},"vulnerabilities":{"found":false,"count":0,"list":[]}}` + "\n",
				Detail: unreadableShape("cargo audit", "exit status 1"),
				Err:    errors.New("exit status 1"),
			},
			want: "names no finding",
		},
		{
			name: "deny claims",
			result: executil.Result{
				Name:   "cargo deny(engine)",
				Output: `{"type":"diagnostic","fields":{"code":"banned","severity":"error","message":"crate is explicitly banned","graphs":[{"name":"openssl","version":"0.10.55"}]}}` + "\n",
				Detail: "Cargo.toml:1  banned  crate is explicitly banned\n  openssl 0.10.55\n",
				Err:    errors.New("exit status 2"),
			},
			want: "explicitly banned",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rows := resultRows(root, []executil.Result{tc.result})
			if len(rows) != 1 {
				t.Fatalf("resultRows returned %d rows, want 1", len(rows))
			}
			row := rows[0]
			if row.Log == "" {
				t.Fatal("no log was written, so the fallback this test guards cannot fire")
			}

			lines := failureLines(reportsDir(root), row)
			joined := strings.Join(lines, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("the row's own detail did not reach the comment:\n%s", joined)
			}
			if strings.Contains(joined, `{"`) {
				t.Errorf("the JSON report was quoted:\n%s", joined)
			}

			// The same row with no detail does quote the log, which is what
			// makes the assertion above about this log and not an empty one.
			row.Detail = nil
			if fallback := strings.Join(failureLines(reportsDir(root), row), "\n"); !strings.Contains(fallback, `{"`) {
				t.Errorf("the log holds no JSON, so nothing was proved:\n%s", fallback)
			}
		})
	}
}

// unreadableShape is the message internal/rust renders for a failing run whose
// report names no claim, restated here as the text this layer has to carry
// through untouched.
func unreadableShape(gate, status string) string {
	return fmt.Sprintf("%s failed (%s) and its JSON report names no finding; the check's log holds the report.", gate, status)
}
