package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/ledger"
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
