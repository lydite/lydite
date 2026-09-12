package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/typescript"
	"lydite/lydite/internal/ui"
)

// historyOn reads back every record the state branch holds for the month a
// recording taken now lands in.
//
// Read off the branch rather than out of a return value, because what this
// feature is worth is entirely a property of what is on the branch: a record
// that reached a struct and not the remote is a data point nobody will ever
// see again.
func historyOn(t *testing.T, root string) []ledger.Record {
	t.Helper()
	ctx := context.Background()
	if r := executil.RunQuiet(ctx, root, "git", "fetch", "origin", gitstate.BranchName); !r.Ok() {
		t.Fatalf("the %s branch does not exist, so nothing was ever appended to it: %v\n%s",
			gitstate.BranchName, r.Err, r.Stderr)
	}
	path := ledger.Dir + "/" + time.Now().UTC().Format("2006-01") + ".ndjson"
	r := executil.RunQuiet(ctx, root, "git", "show", "origin/"+gitstate.BranchName+":"+path)
	if !r.Ok() {
		t.Fatalf("%s is not on %s: %v", path, gitstate.BranchName, r.Err)
	}
	var recs []ledger.Record
	for _, line := range strings.Split(strings.TrimSpace(r.Output), "\n") {
		if line == "" {
			continue
		}
		var rec ledger.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("%s holds a line that is not a record: %v", path, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// headCommit is the commit currently checked out, which a test uses to rewind
// to a point it wants to build a different history from.
func headCommit(t *testing.T, root string) string {
	t.Helper()
	r := executil.RunQuiet(context.Background(), root, "git", "rev-parse", "HEAD")
	if !r.Ok() {
		t.Fatalf("git rev-parse HEAD: %v", r.Err)
	}
	return strings.TrimSpace(r.Output)
}

func measureAndRecord(t *testing.T, root string) string {
	t.Helper()
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	out, errOut, err := runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("recording: %v\n%s\n%s", err, out, errOut)
	}
	return out
}

// The whole feature, end to end against a real repository: a recording lands a
// history record on the state branch beside the baseline, carrying the scalars
// nothing can recompute once this commit has been squashed away.
func TestARecordingLandsThisCommitsScalars(t *testing.T) {
	root := gateRepo(t)
	out := measureAndRecord(t, root)

	if row := jsonRows(t, out)["history"]; !strings.Contains(row.Value, "1 record(s) appended") {
		t.Errorf("history = %+v, want it to say a record was appended", row)
	}

	recs := historyOn(t, root)
	if len(recs) != 1 {
		t.Fatalf("the branch holds %d records, want the one this recording appended", len(recs))
	}
	rec := recs[0]
	head, err := gitstate.DescribeCommit(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// A commit, and not the tree a baseline is keyed by. A baseline answers
	// "what was measured for this content", so a pull request and the commit
	// it becomes share one deliberately; history is a sequence of events.
	if rec.Kind != ledger.KindEntry || rec.Commit != head.SHA {
		t.Errorf("the record = %s/%s, want an entry for %s", rec.Kind, rec.Commit, head.SHA)
	}
	if rec.Branch != "main" {
		t.Errorf("the record names branch %q, want main", rec.Branch)
	}
	if rec.Tree != head.Tree {
		t.Errorf("the record names tree %q, want %q, which is what joins it to the baseline", rec.Tree, head.Tree)
	}
	svc := rec.Components["svc"]
	if svc.Coverage == nil || svc.Coverage.Total != 2 || svc.Coverage.Covered != 1 {
		t.Errorf("svc coverage = %+v, want the one of two statements the fixture covers", svc.Coverage)
	}
	if svc.CRAP == nil {
		t.Error("svc carries no CRAP scalars, which are recorded and never gated")
	}
	// The counts no coverage report carries, and the reason a Go component's
	// instrumented suite goes through the pinned wrapper at all.
	if svc.Tests == nil || svc.Tests.Total != 1 || svc.Tests.Failed != 0 {
		t.Errorf("svc tests = %+v, want the one passing test the fixture has", svc.Tests)
	}
	if svc.Producer == "" {
		t.Error("svc names no producer, so nothing downstream can tell a step in the line from a change of instrument")
	}
}

// A commit whose predecessor was never recorded gets an explicit gap beside
// its entry, saying how many recordings are missing.
//
// The width is the half a reader cannot work out for themselves: the parent
// chain in the records already shows THAT there is a hole, and only the
// repository knows how wide. That division is what makes a gap recordable at
// all — the append that failed wrote nothing, so the record of it has to be
// written by the next successful one, out of git history.
func TestAnUnrecordedCommitIsRecordedAsAGap(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) { gitIn(t, root, args...) }
	measureAndRecord(t, root)
	first, err := gitstate.DescribeCommit(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// One commit that nothing records — a recording job that failed, or a
	// push race that never landed.
	write(t, root, "svc/notes.md", "not code\n")
	run("add", "-A")
	run("commit", "-m", "unrecorded")

	write(t, root, "svc/more.md", "still not code\n")
	run("add", "-A")
	run("commit", "-m", "recorded again")
	out := measureAndRecord(t, root)

	if row := jsonRows(t, out)["history"]; !strings.Contains(row.Value, "gap") {
		t.Errorf("history = %+v, want it to say one of the records was a gap", row)
	}
	recs := historyOn(t, root)
	if len(recs) != 3 {
		t.Fatalf("the branch holds %d records, want the first entry, a gap and the second entry", len(recs))
	}
	gap := recs[1]
	if gap.Kind != ledger.KindGap || gap.Gap == nil {
		t.Fatalf("the second record = %+v, want a gap", gap)
	}
	if gap.Gap.From != first.SHA {
		t.Errorf("the gap is measured from %q, want the last recorded commit %q", gap.Gap.From, first.SHA)
	}
	if gap.Gap.Missing != 1 {
		t.Errorf("the gap says %d commits are missing, want 1", gap.Gap.Missing)
	}
	// The chain is self-checking without the gap record: every entry names
	// its parent, so a reader that has only the partition still sees the hole.
	if recs[2].Parent == recs[0].Commit {
		t.Error("the second entry's parent is the first entry's commit, so the fixture recorded no hole at all")
	}
}

// A contiguous recording writes no gap. A break claimed where there is none is
// worse than a missed one: it renders as a hole in the line and there is
// nothing anybody can do about it.
func TestAContiguousRecordingWritesNoGap(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) { gitIn(t, root, args...) }
	measureAndRecord(t, root)
	write(t, root, "svc/notes.md", "not code\n")
	run("add", "-A")
	run("commit", "-m", "next")
	measureAndRecord(t, root)

	for _, rec := range historyOn(t, root) {
		if rec.Kind == ledger.KindGap {
			t.Fatalf("a gap was recorded between two consecutive recordings: %+v", rec.Gap)
		}
	}
}

// The first recording a repository ever makes is not a gap. Nothing precedes
// it, and claiming a break there would put one at the start of every line.
func TestTheFirstRecordingIsNotAGap(t *testing.T) {
	root := gateRepo(t)
	measureAndRecord(t, root)
	for _, rec := range historyOn(t, root) {
		if rec.Kind == ledger.KindGap {
			t.Fatalf("the first recording on a fresh branch wrote a gap: %+v", rec.Gap)
		}
	}
}

// Recording the same commit twice leaves one record. A retried push, a re-run
// workflow and a second invocation all describe the same commit, and a history
// that counted them twice would show a day with twice the recordings it had.
func TestRecordingACommitTwiceLeavesOneRecord(t *testing.T) {
	root := gateRepo(t)
	measureAndRecord(t, root)
	out := measureAndRecord(t, root)
	if row := jsonRows(t, out)["history"]; !strings.Contains(row.Value, "already recorded") {
		t.Errorf("history = %+v, want it to say the record was already there", row)
	}
	if recs := historyOn(t, root); len(recs) != 1 {
		t.Errorf("the branch holds %d records after recording one commit twice, want 1", len(recs))
	}
}

// A run whose suite failed establishes no baseline and still records what
// happened. The two are different policies over one branch: a baseline is a
// cache, refused whenever it would be partial because a partial one reads as a
// cache hit and gates every later change on nothing; a record is not
// recomputable at all, and a commit whose build broke is exactly the one whose
// test counts a history most wants.
func TestAFailingSuiteRecordsItsTestCountsAndNoBaseline(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) { gitIn(t, root, args...) }
	write(t, root, "svc/lib_test.go",
		"package svc\n\nimport \"testing\"\n\nfunc TestKeep(t *testing.T) {\n\tif Keep(1) != 2 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n\nfunc TestBroken(t *testing.T) {\n\tt.Fatal(\"deliberate\")\n}\n")
	run("add", "-A")
	run("commit", "-m", "a suite that fails")

	// The measuring run fails, which is the point: it is the recording that
	// must still happen.
	if _, _, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err == nil {
		t.Fatal("the fixture's suite passed, so this asserts nothing")
	}
	out, errOut, err := runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("recording: %v\n%s\n%s", err, out, errOut)
	}
	rows := jsonRows(t, out)
	if row := rows["record"]; row.Status != "unmeasured" {
		t.Errorf("record = %+v, want no baseline from a run whose suite failed", row)
	}
	// The status and not the wording: "not appended — …" contains "appended".
	if row := rows["history"]; row.Status != "context" || strings.HasPrefix(row.Value, "not ") {
		t.Fatalf("history = %+v, want the record appended anyway", row)
	}

	recs := historyOn(t, root)
	if len(recs) != 1 {
		t.Fatalf("the branch holds %d records, want the one this recording appended", len(recs))
	}
	svc := recs[0].Components["svc"]
	if svc.Tests == nil || svc.Tests.Total != 2 || svc.Tests.Failed != 1 {
		t.Errorf("svc tests = %+v, want two tests of which one failed", svc.Tests)
	}
	// And no coverage, because a report written by a suite that stopped early
	// describes an unfinished run. The counts describe exactly what happened;
	// the coverage figure would not.
	if svc.Coverage != nil {
		t.Errorf("svc recorded coverage = %+v from a suite that failed", svc.Coverage)
	}
	if r := executil.RunQuiet(context.Background(), root, "git", "show",
		"origin/"+gitstate.BranchName+":"+gitstate.StatePath(recs[0].Tree)); r.Ok() {
		t.Error("a baseline was recorded for a tree whose suite failed")
	}
}

// rejectPushes makes the repository's origin refuse every push, which is what
// a lost race on the shared state branch looks like from here.
func rejectPushes(t *testing.T, root string) {
	t.Helper()
	r := executil.RunQuiet(context.Background(), root, "git", "remote", "get-url", "origin")
	if !r.Ok() {
		t.Fatalf("git remote get-url: %v", r.Err)
	}
	hook := filepath.Join(strings.TrimSpace(r.Output), "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- a hook this test needs to be executable
		t.Fatal(err)
	}
}

// A push that never lands fails the command when a baseline was being landed:
// recording is the one thing this command exists to do, and a write that never
// landed must never be reported as recorded.
func TestAPushThatNeverLandsFailsTheRecording(t *testing.T) {
	root := gateRepo(t)
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	rejectPushes(t, root)

	out, _, err := runRecordCmd(t, root, "--json")
	if err == nil {
		t.Fatalf("record reported success even though every push was rejected\n%s", out)
	}
	rows := jsonRows(t, out)
	if row := rows["record"]; row.Status != "fail" || !strings.Contains(row.Value, "did not land") {
		t.Errorf("record = %+v, want a failure naming the write that did not land", row)
	}
	// The history row says the append is missing and points at what fills it,
	// rather than repeating the baseline's own reason.
	if row := rows["history"]; row.Status != "unmeasured" || !strings.Contains(row.Value, "records the gap") {
		t.Errorf("history = %+v, want it to say the next recording records the gap", row)
	}
}

// A recording carrying no baseline is writing only the ledger, and a failed
// append is never a failing row: the branch is shared and busy, a push race is
// routine, and failing a consumer's build over one would erode trust in a gate
// that is otherwise about their code. The next successful append records the
// gap.
func TestAPushThatNeverLandsDoesNotFailAHistoryOnlyRecording(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) { gitIn(t, root, args...) }
	// A suite that fails establishes no baseline by construction and still has
	// its test counts, which is the recording that writes history alone.
	write(t, root, "svc/lib_test.go",
		"package svc\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) {\n\tt.Fatal(\"deliberate\")\n}\n")
	run("add", "-A")
	run("commit", "-m", "a suite that fails")
	if _, _, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err == nil {
		t.Fatal("the fixture's suite passed, so this asserts nothing")
	}
	rejectPushes(t, root)

	out, _, err := runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("a failed history append failed the command: %v\n%s", err, out)
	}
	rows := jsonRows(t, out)
	// Present and saying its own thing: an absent row is not "not a failure",
	// it is a reader told nothing about the baseline at all.
	if row := rows["record"]; row.Status != "unmeasured" || !strings.Contains(row.Value, "nothing to record") {
		t.Errorf("record = %+v, want the baseline's own answer rather than a push failure", row)
	}
	if row := rows["history"]; row.Status != "unmeasured" || !strings.Contains(row.Value, "records the gap") {
		t.Errorf("history = %+v, want an amber row saying the next recording records the gap", row)
	}
}

// A component contributing no scalar at all is a point on no line, and is
// dropped. One carrying any single scalar is kept: coverage without a score is
// every non-Go component, and test counts without coverage is every component
// whose suite went red.
func TestOnlyAComponentWithNoScalarAtAllIsDropped(t *testing.T) {
	doc := measurementsDoc{
		Components: map[string]componentMeasurement{
			"measured": {Entry: producing(3, 4, "go 1.26")},
			"scored": {Entry: gitstate.Entry{Producer: "go 1.26"},
				CRAP: &gitstate.CRAPEntry{Above: 0, Worst: 12.5}},
			"empty": {Entry: gitstate.Entry{Producer: "go 1.26"}},
		},
		Tests: map[string]junit.Counts{"red": {Total: 9, Failed: 2}},
	}
	got := historyComponents(doc, nil)

	for _, name := range []string{"measured", "scored", "red"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s was dropped, but it carries a scalar", name)
		}
	}
	if _, ok := got["empty"]; ok {
		t.Error("a component carrying no scalar at all was kept")
	}
	if c := got["measured"]; c.Coverage == nil || c.Coverage.Covered != 3 {
		t.Errorf("measured = %+v, want its line counts", c.Coverage)
	}
	if c := got["scored"]; c.CRAP == nil || c.Coverage != nil {
		t.Errorf("scored = %+v, want a score and no coverage", c)
	}
	if c := got["red"]; c.Tests == nil || c.Tests.Failed != 2 {
		t.Errorf("red = %+v, want the counts from a suite that failed", c.Tests)
	}
}

// The branch a recording is filed under is the caller's to state, and the flag
// is what states it. A checkout that names no branch is the normal shape of a
// CI job, so the flag is the path that has to work.
func TestTheBranchFlagNamesTheLineARecordingJoins(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) { gitIn(t, root, args...) }
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	// Detached, which is what the flag exists for: discovery answers nothing
	// here, and without the flag nothing would be appended at all.
	run("checkout", "--quiet", "--detach", "HEAD")

	out, errOut, err := runRecordCmd(t, root, "--json", "--branch", "release/1.x")
	if err != nil {
		t.Fatalf("recording: %v\n%s\n%s", err, out, errOut)
	}
	if row := jsonRows(t, out)["history"]; row.Status != "context" {
		t.Fatalf("history = %+v, want the stated branch to have carried the append", row)
	}
	recs := historyOn(t, root)
	if len(recs) != 1 || recs[0].Branch != "release/1.x" {
		t.Fatalf("the branch holds %+v, want one record on release/1.x", recs)
	}
}

// A checkout that names no branch and a caller that states none appends
// nothing, and says which flag fills the gap. Silence here would be a
// repository that records history on no run and never says why.
func TestARecordingWithNoBranchSaysWhichFlagNamesIt(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) { gitIn(t, root, args...) }
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	run("checkout", "--quiet", "--detach", "HEAD")

	out, _, err := runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("recording: %v\n%s", err, out)
	}
	row := jsonRows(t, out)["history"]
	if row.Status != "unmeasured" {
		t.Fatalf("history = %+v, want an amber row: nothing can be filed under a branch nobody named", row)
	}
	if !strings.Contains(row.Value, gitstate.BranchFlag) {
		t.Errorf("history = %q, want it to name the flag that fills the gap", row.Value)
	}
	// The baseline still lands: the branch is the history's question, not the
	// baseline's.
	if r := jsonRows(t, out)["record"]; r.Status != "pass" {
		t.Errorf("record = %+v, want the baseline recorded regardless", r)
	}
}

// A fold whose components carry no scalar appends nothing. A record naming a
// commit and holding no number is a point on no line, and writing one would
// make the history claim a measurement that was never taken.
func TestHistoryIsNotAppendedForAFoldWithNoScalars(t *testing.T) {
	records, why := historyRecords(context.Background(), t.TempDir(), "main",
		measurementsDoc{Components: map[string]componentMeasurement{
			"api": {Entry: gitstate.Entry{Producer: "go 1.26"}},
		}}, nil, nil)
	if records != nil {
		t.Error("a fold carrying no scalar produced records")
	}
	if !strings.Contains(why, "no component produced a scalar") {
		t.Errorf("why = %q, want it to say no scalar was produced", why)
	}
}

// A commit whose recorded predecessor is not an ancestor is a break whose
// width cannot be established — a force-push, or an unrelated history. Saying
// so is the whole of what the record is for; a width invented here would be
// worse than the honest absence.
func TestAPredecessorThatIsNotAnAncestorIsAGapOfUnknownWidth(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) { gitIn(t, root, args...) }
	base := headCommit(t, root)

	// A commit recorded and then rewritten away, which is what a force-push
	// leaves behind: the branch's last record is no longer on it.
	write(t, root, "svc/notes.md", "recorded, then rewritten\n")
	run("add", "-A")
	run("commit", "-m", "the commit that gets rewritten")
	measureAndRecord(t, root)

	run("reset", "--quiet", "--hard", base)
	write(t, root, "svc/other.md", "a different line of history\n")
	run("add", "-A")
	run("commit", "-m", "what replaced it")
	measureAndRecord(t, root)

	var gap *ledger.Gap
	for _, rec := range historyOn(t, root) {
		if rec.Kind == ledger.KindGap {
			gap = rec.Gap
		}
	}
	if gap == nil {
		t.Fatal("no gap was recorded across an unrelated history")
		return
	}
	if gap.Missing != 0 || !strings.Contains(gap.Reason, "not an ancestor") {
		t.Errorf("the gap = %+v, want an unestablished width and the reason why", gap)
	}
}

// scanDocument plants the document `lydite scan` writes into dir, holding the
// rows and the located claims handed in.
//
// Written through ui.Report rather than as literal JSON, so a test plants the
// shape scan actually writes rather than a hand-built one that agrees with it
// until one of the two changes.
func scanDocument(t *testing.T, dir string, rows []ui.Row, findings ...finding.Finding) {
	t.Helper()
	rep := ui.NewReport("scan")
	for _, row := range rows {
		rep.Add(row)
	}
	rep.AddFindings(findings...)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, documentName("scan"))) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
}

// gosecFinding is one claim attributed to a component, as a scanner reports it.
func gosecFinding(component, site string) finding.Finding {
	return finding.Finding{
		Gate: golang.GateGosec, Component: component, Path: component + "/lib.go", Line: 5,
		Rule: "G401", Severity: "HIGH", Message: "weak cryptographic primitive",
		Site: "G401\x1f" + site, Row: golang.GateGosec + "(" + component + ")",
	}
}

// A gate that ran and found nothing records nought; a gate that does not apply
// to the component's language records no key at all.
//
// The distinction is the whole of why the count is a per-gate map rather than
// one integer. A single total cannot hold it, and a series that read an absence
// as a zero would draw a clean line through a scanner that never ran.
func TestAScannerGateThatFoundNothingRecordsZeroAndOneThatDoesNotApplyRecordsNoKey(t *testing.T) {
	root := gateRepo(t)
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	// One claim against the Go component, and one root-scoped claim Semgrep
	// made about the repository rather than about any component.
	scanDocument(t, filepath.Join(root, runner.ReportDir), nil,
		gosecFinding("svc", "sha1.New()"),
		finding.Finding{
			Gate: semgrep.Gate, Path: "svc/lib.go", Line: 2,
			Rule: "go.lang.security.audit", Message: "tainted input",
			Site: "go.lang.security.audit\x1fn + 1", Row: semgrep.Gate,
		})

	out, errOut, err := runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("recording: %v\n%s\n%s", err, out, errOut)
	}
	recs := historyOn(t, root)
	if len(recs) != 1 {
		t.Fatalf("the branch holds %d records, want the one this recording appended", len(recs))
	}
	svc := recs[0].Components["svc"]
	if got := svc.Findings[golang.GateGosec]; got != 1 {
		t.Errorf("gosec = %d, want the one claim the scan document holds", got)
	}
	// Zero and present: govulncheck applies to a Go component, so a component
	// it found nothing in is a measured nought rather than a gap in the series.
	got, ok := svc.Findings[golang.GateGovulncheck]
	if !ok || got != 0 {
		t.Errorf("govulncheck = %d (present %v), want a recorded nought — the gate applies and found nothing", got, ok)
	}
	// Absent: no Biome runs over a Go component, so a nought here would claim
	// a scanner looked at code it cannot read.
	if got, ok := svc.Findings[typescript.GateBiome]; ok {
		t.Errorf("biome = %d, want no key at all: the gate does not apply to this component's language", got)
	}
	// Root-scoped, and beside the components rather than inside one: nothing
	// may decide which component a repository-wide claim belongs to.
	if got := recs[0].RootFindings[semgrep.Gate]; got != 1 {
		t.Errorf("semgrep = %d, want the one root-scoped claim", got)
	}
	if _, ok := svc.Findings[semgrep.Gate]; ok {
		t.Error("a root-scoped claim was attributed to a component")
	}
}

// A scan that went red still records what it found, out of a directory holding
// no measurements at all.
//
// Both halves are the shape the recording job is handed: the scan uploads its
// own report directory beside the shards', and a scan on the default branch
// that found something exits non-zero. A red scan there is the most interesting
// thing a finding history can hold, and no later run can fill the hole — the
// next push is a different tree.
func TestAFailingScanInItsOwnDirectoryStillRecordsWhatItFound(t *testing.T) {
	root := gateRepo(t)
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	scans := t.TempDir()
	scanDocument(t, scans,
		[]ui.Row{{Status: ui.StatusFail, Label: "gosec(svc)", Value: "failed"}},
		gosecFinding("svc", "sha1.New()"), gosecFinding("svc", "md5.New()"))

	out, errOut, err := runRecordCmd(t, root, "--json", "--reports", scans)
	if err != nil {
		t.Fatalf("recording: %v\n%s\n%s", err, out, errOut)
	}
	recs := historyOn(t, root)
	if len(recs) != 1 {
		t.Fatalf("the branch holds %d records, want the one this recording appended", len(recs))
	}
	if got := recs[0].Components["svc"].Findings[golang.GateGosec]; got != 2 {
		t.Errorf("gosec = %d, want both claims the failing scan made", got)
	}
}

// A report directory holding a scan and no measurements is the expected shape
// of the scan job, not a malfunction, so its row is context rather than amber.
//
// Amber is the tag for a check that did not run, and a row that renders the
// ordinary output of a job with it trains a reader to skip the one tag that
// exists to be noticed.
func TestADirectoryHoldingOnlyAScanIsNotReportedAsUnmeasured(t *testing.T) {
	dir := t.TempDir()
	scanDocument(t, dir, nil, gosecFinding("svc", "sha1.New()"))

	rep := ui.NewReport("record")
	read := readReports(rep, []string{dir})

	if len(read.docs) != 0 || len(read.found) != 1 || !read.scanned {
		t.Fatalf("read = %d measurements, %d findings, scanned %v; want the scan alone",
			len(read.docs), len(read.found), read.scanned)
	}
	rows := rep.Rows()
	if len(rows) != 1 {
		t.Fatalf("%d rows for one directory, want exactly one saying what came out of it", len(rows))
	}
	if rows[0].Status != ui.StatusContext {
		t.Errorf("read row = %+v, want context: a scan with no measurements beside it is what the scan job uploads", rows[0])
	}
	if !strings.Contains(rows[0].Value, "1 finding(s)") {
		t.Errorf("read row = %q, want it to say what the directory held", rows[0].Value)
	}
	// And silent about the measurements nobody wrote here, for the reason the
	// row is context: a raw "no such file" under the ordinary output of a job
	// is how a detail line stops being read.
	if len(rows[0].Detail) != 0 {
		t.Errorf("read row detail = %v, want nothing said about measurements this job never writes", rows[0].Detail)
	}
}

// A report directory holding neither document still says why.
//
// The absence is suppressed from a row that held the other document, and this
// is the one row where it is the whole of what there is to report: a directory
// that yielded nothing, with no reason, is a recording quietly folding less than
// the caller named.
func TestAReportDirectoryHoldingNeitherDocumentStillSaysWhy(t *testing.T) {
	dir := t.TempDir()

	rep := ui.NewReport("record")
	read := readReports(rep, []string{dir})

	if len(read.docs) != 0 || read.scanned {
		t.Fatalf("read = %d measurements, scanned %v; want nothing from an empty directory", len(read.docs), read.scanned)
	}
	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusUnmeasured {
		t.Fatalf("rows = %+v, want one amber row", rows)
	}
	if len(rows[0].Detail) == 0 {
		t.Error("a directory that yielded nothing was reported without a reason")
	}
}

// No scan document means no finding counts at all, and never a nought per
// applicable gate.
//
// A nought says the gate ran and found nothing. Inventing one for a scan that
// never happened is the single thing this channel must not do: it would draw a
// clean line through every commit whose scan job died.
func TestNoScanDocumentRecordsNoFindingCountAtAll(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "svc", Dir: "svc", Runner: "go-test"},
	}}
	perComponent, root := findingCounts(decl, config.Default(), nil, false)
	if perComponent != nil || root != nil {
		t.Errorf("findingCounts = %v / %v, want nothing recorded for a recording that read no scan", perComponent, root)
	}
	// And with a scan that read clean, the same declaration records noughts.
	perComponent, root = findingCounts(decl, config.Default(), nil, true)
	if got, ok := perComponent["svc"][golang.GateGosec]; !ok || got != 0 {
		t.Errorf("gosec = %d (present %v), want a recorded nought for a clean scan", got, ok)
	}
	if got, ok := root[semgrep.Gate]; !ok || got != 0 {
		t.Errorf("semgrep = %d (present %v), want a recorded nought for a clean scan", got, ok)
	}
}

// A root-scoped claim is recorded even when the gate that makes them is
// switched off, rather than panicking on a map the configuration said would
// never be needed.
//
// Semgrep off means scan runs it over nothing, so this is the shape no run
// produces — which is exactly why the map has to be created on demand: a count
// arriving for a gate the configuration did not expect must be recorded, and a
// write to a nil map is a crash in the one job holding a token that can push.
func TestARootScopedClaimIsRecordedWithItsGateSwitchedOff(t *testing.T) {
	cfg := config.Default()
	cfg.Semgrep.Enabled = false
	_, root := findingCounts(component.File{}, cfg, []finding.Finding{{
		Gate: semgrep.Gate, Path: "svc/lib.go", Line: 2, Message: "tainted input",
		Site: "rule\x1fn + 1",
	}}, true)
	if got := root[semgrep.Gate]; got != 1 {
		t.Errorf("semgrep = %d, want the claim recorded rather than dropped or panicked on", got)
	}
}

// A report directory holding measurements and no scan says nothing about a scan.
//
// The detail line is where a reader is told something went wrong, so a missing
// scan document reported there would put "no such file" under every shard of
// every recording — which is how a detail line stops being read.
func TestAReportDirectoryWithNoScanDocumentSaysNothingAboutOne(t *testing.T) {
	root := t.TempDir()
	if err := writeMeasurements(root, measurementsDoc{Tree: "deadbeef"}); err != nil {
		t.Fatal(err)
	}
	dir := reportsDir(root)

	rep := ui.NewReport("record")
	read := readReports(rep, []string{dir})

	if len(read.docs) != 1 || read.scanned {
		t.Fatalf("read = %d measurements, scanned %v; want the measurements alone", len(read.docs), read.scanned)
	}
	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusContext {
		t.Fatalf("rows = %+v, want one context row", rows)
	}
	if len(rows[0].Detail) != 0 {
		t.Errorf("read row detail = %v, want nothing said about a scan document that was never written", rows[0].Detail)
	}
}

// A document that is there and will not parse is reported, and never read as
// one that was never written.
//
// The two are opposite answers. An absent document is the shape of a job that
// writes the other one, and says nothing a reader must act on; a document that
// cannot be read is a measurement or a set of counts this recording was meant
// to hold and silently does not.
func TestADocumentThatWillNotParseIsReportedRatherThanReadAsAbsent(t *testing.T) {
	// Measurements beside a scan that will not parse: the directory still
	// yields its components, so the row is context and the reason travels in
	// its detail rather than being the whole of it.
	root := t.TempDir()
	if err := writeMeasurements(root, measurementsDoc{Tree: "deadbeef"}); err != nil {
		t.Fatal(err)
	}
	dir := reportsDir(root)
	if err := os.WriteFile(filepath.Join(dir, documentName("scan")), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep := ui.NewReport("record")
	read := readReports(rep, []string{dir})

	if read.scanned {
		t.Error("a scan document that could not be read was counted as a scan that ran")
	}
	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusContext {
		t.Fatalf("rows = %+v, want one context row — the measurements were still folded", rows)
	}
	if len(rows[0].Detail) == 0 {
		t.Error("a scan document that could not be read was reported as though none was written")
	}

	// And neither document readable: the row is amber, and every reason
	// reaches it.
	both := t.TempDir()
	for _, name := range []string{measurementsName, documentName("scan")} {
		if err := os.WriteFile(filepath.Join(both, name), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep = ui.NewReport("record")
	read = readReports(rep, []string{both})
	if len(read.docs) != 0 || read.scanned {
		t.Fatalf("read = %d measurements, scanned %v; want nothing from two unreadable documents",
			len(read.docs), read.scanned)
	}
	rows = rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusUnmeasured {
		t.Fatalf("rows = %+v, want one amber row", rows)
	}
	if len(rows[0].Detail) != 2 {
		t.Errorf("the row carries %v, want both documents' reasons", rows[0].Detail)
	}
}

// A component with no language, and one whose language is switched off, record
// no finding count.
//
// Neither has a gate that applies, so a nought for either would say a scanner
// looked at code lydite never offered it — a component declaring its own
// command implies no language at all, and `enabled: false` is the repository
// saying that language's checks do not run.
func TestAComponentWithNoApplicableGateRecordsNoCount(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "raw", Dir: "raw", Command: []string{"make", "check"}},
		{Name: "api", Dir: "api", Runner: "go-test"},
	}}
	cfg := config.Default()
	cfg.Go.Enabled = false
	cfg.Semgrep.Enabled = false

	perComponent, root := findingCounts(decl, cfg, nil, true)

	if len(perComponent) != 0 {
		t.Errorf("findingCounts = %v, want no component recorded: one declares its own command and the other's language is off", perComponent)
	}
	if len(root) != 0 {
		t.Errorf("root = %v, want nothing for a scan whose only root-scoped gate is off", root)
	}
}

// A recording refuses a declaration or a configuration it cannot read, before
// it reads a document.
//
// Both say what may be recorded — which components a complete baseline covers,
// and which of this tree's languages are checked at all — so a recording that
// proceeded without them would decide those questions from the documents whose
// completeness is in question.
func TestARecordingRefusesADeclarationOrConfigurationItCannotRead(t *testing.T) {
	broken := t.TempDir()
	write(t, broken, component.FileName, "components: [unclosed\n")
	if out, _, err := runRecordCmd(t, broken, "--json"); err == nil {
		t.Errorf("a declaration that will not parse was recorded against: %s", out)
	}

	badConfig := t.TempDir()
	write(t, badConfig, component.FileName, "components:\n  - name: api\n    dir: api\n    runner: go-test\n")
	write(t, badConfig, config.FileName, "go: [unclosed\n")
	if out, _, err := runRecordCmd(t, badConfig, "--json"); err == nil {
		t.Errorf("a configuration that will not parse was recorded against: %s", out)
	}
}

// A recording says how much of a scan it counted, and says so when it counted
// nothing at all.
//
// This is the row that was missing when the count first reached production: the
// scan job was green, the recording was green, and an absent finding count in
// the ledger is indistinguishable from a repository with no findings. A count
// that did not happen has to read differently from a count of nothing.
func TestARecordingSaysWhetherItCountedAScan(t *testing.T) {
	root := gateRepo(t)
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}

	// No scan document anywhere, which is every report directory a run without
	// a scan job writes.
	out, errOut, err := runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("recording: %v\n%s\n%s", err, out, errOut)
	}
	row := jsonRows(t, out)["findings"]
	if row.Status != "context" {
		t.Errorf("findings = %+v, want a context row: the count is not a gate", row)
	}
	// The reason, not just the absence. A recording that read no scan and one
	// whose components have no applicable gate both counted nothing, and only
	// the reason says which — the first is a workflow that delivered no
	// document, the second a repository with nothing to count.
	if !strings.Contains(row.Value, "not counted") || !strings.Contains(row.Value, documentName("scan")) {
		t.Errorf("findings = %q, want it to say no count was made and name the document that was missing", row.Value)
	}

	// And with a scan beside the measurements, the same row says what it counted.
	scanDocument(t, filepath.Join(root, runner.ReportDir), nil, gosecFinding("svc", "sha1.New()"))
	out, errOut, err = runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("recording with a scan: %v\n%s\n%s", err, out, errOut)
	}
	row = jsonRows(t, out)["findings"]
	if !strings.Contains(row.Value, "gate(s) counted") {
		t.Errorf("findings = %q, want it to say how many gates were counted", row.Value)
	}
	if strings.Contains(row.Value, "not counted") {
		t.Errorf("findings = %q, want a count rather than its absence", row.Value)
	}
}

// The findings row distinguishes four outcomes, and each needs its own reason.
//
// A recording that read no scan, one whose components have no gate that reports
// findings, and one that counted are three different facts, and the row is the
// only place a reader learns which. The two zero cases in particular must not
// collapse: the first is a workflow that delivered no document — the defect
// #134 fixes — and the second is a repository with nothing to count, which is
// a correct and permanent state for a consumer declaring only raw commands.
func TestTheFindingsRowNamesWhichOfTheZeroesItIs(t *testing.T) {
	goGates := map[string]map[string]int{"api": {golang.GateGosec: 0, golang.GateGovulncheck: 2}}

	for _, tc := range []struct {
		name         string
		perComponent map[string]map[string]int
		root         map[string]int
		scanned      bool
		want         string
	}{
		{"no scan was read", nil, nil, false, documentName("scan")},
		{"nothing applies", nil, nil, true, "no declared component has a gate"},
		// Gates but no root-scoped count: a repository with Semgrep switched
		// off still counted something, so this is not one of the zero cases.
		{"gates only", goGates, nil, true, "gate(s) counted"},
		// And the mirror: a root-scoped count with no per-component gate, which
		// is every component declaring its own command while Semgrep runs.
		{"root only", nil, map[string]int{semgrep.Gate: 3}, true, "gate(s) counted"},
		{"both", goGates, map[string]int{semgrep.Gate: 3}, true, "gate(s) counted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := findingsRow(tc.perComponent, tc.root, tc.scanned)
			// Never a verdict: the count is not a gate, and an amber row here
			// would colour every recording a consumer makes without a scan job.
			if row.Status != ui.StatusContext {
				t.Errorf("status = %s, want context", row.Status)
			}
			if !strings.Contains(row.Value, tc.want) {
				t.Errorf("row = %q, want it to say %q", row.Value, tc.want)
			}
		})
	}
	// The counted row says how much, not merely that it counted.
	row := findingsRow(goGates, map[string]int{semgrep.Gate: 3}, true)
	for _, want := range []string{"2 gate(s)", "1 component(s)", "1 root-scoped"} {
		if !strings.Contains(row.Value, want) {
			t.Errorf("row = %q, want it to carry %q", row.Value, want)
		}
	}
}
