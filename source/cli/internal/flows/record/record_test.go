package recordflow

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	ledgerstages "lydite/lydite/internal/stages/ledger"
	recordstages "lydite/lydite/internal/stages/record"
)

// recordReader is a ReportReader over documents held in memory, keyed by
// directory. A directory with no document of a kind answers the absence the
// real reader gives, and every fold answers the first directory it is given.
type recordReader struct {
	measurements map[string]recordstages.Measurements
	mutants      map[string]recordstages.Mutants
	// foldedMutants counts the FoldMutants calls, which only bind-mutants
	// makes.
	foldedMutants int
}

func (r *recordReader) ReadMeasurements(dir string) (recordstages.Measurements, error) {
	m, ok := r.measurements[dir]
	if !ok {
		return recordstages.Measurements{}, recordAbsent(dir, "measurements.json")
	}
	return m, nil
}

func (r *recordReader) ReadScan(dir string) (recordstages.Scan, error) {
	return recordstages.Scan{}, recordAbsent(dir, "scan.json")
}

func (r *recordReader) ReadMutants(dir string) (recordstages.Mutants, error) {
	m, ok := r.mutants[dir]
	if !ok {
		return recordstages.Mutants{}, recordAbsent(dir, "mutation.json")
	}
	return m, nil
}

func (r *recordReader) FoldMeasurements(dirs []string) (recordstages.Measurements, error) {
	return r.measurements[dirs[0]], nil
}

func (r *recordReader) FoldMutants(dirs []string) (recordstages.Mutants, error) {
	r.foldedMutants++
	return r.mutants[dirs[0]], nil
}

// recordAbsent is the error a document that is not there reads as.
func recordAbsent(dir, name string) error {
	return &os.PathError{Op: "open", Path: filepath.Join(dir, name), Err: os.ErrNotExist}
}

// recordGit runs git in dir and fails the test when it does not succeed.
func recordGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := executil.RunQuiet(context.Background(), dir, "git", args...)
	if !r.Ok() {
		t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
	}
	return strings.TrimSpace(r.Output)
}

// recordRepo is a repository declaring one Go component, svc, with its one
// commit pushed to a file:// origin — the remote the state branch is fetched
// from and pushed to — and the tree it checks out.
func recordRepo(t *testing.T) (dir, origin, tree string) {
	t.Helper()
	dir = t.TempDir()
	files := map[string]string{
		component.FileName: "components:\n  - name: svc\n    dir: svc\n    runner: go-test\n    args: [\"./...\"]\n",
		"svc/go.mod":       "module svc\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recordGit(t, dir, "init", "--quiet", "-b", "main")
	recordGit(t, dir, "config", "user.email", "t@example.com")
	recordGit(t, dir, "config", "user.name", "t")
	recordGit(t, dir, "add", "-A")
	recordGit(t, dir, "commit", "--quiet", "-m", "measured")
	origin = t.TempDir()
	recordGit(t, origin, "init", "--quiet", "--bare", "-b", "main", ".")
	recordGit(t, dir, "remote", "add", "origin", "file://"+origin)
	recordGit(t, dir, "push", "--quiet", "-u", "origin", "main")
	return dir, origin, recordGit(t, dir, "rev-parse", "HEAD^{tree}")
}

// recordRejectPushes makes origin refuse every push, which is what a lost
// race on the shared state branch looks like from the recording's side.
func recordRejectPushes(t *testing.T, origin string) {
	t.Helper()
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- a hook this test needs to be executable
		t.Fatal(err)
	}
}

// recordStateBranch is what origin holds for the state branch, empty when
// nothing ever reached it.
func recordStateBranch(t *testing.T, origin string) string {
	t.Helper()
	return recordGit(t, origin, "ls-remote", ".", "refs/heads/"+gitstate.BranchName)
}

// recordMeasured is one directory's measurements of svc on tree, with the
// snapshot a baseline lands.
func recordMeasured(tree string) recordstages.Measurements {
	entry := gitstate.Entry{LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "go 1.26"}
	return recordstages.Measurements{
		Tree:       tree,
		Components: map[string]recordstages.Measurement{"svc": {Entry: entry}},
		Snapshot:   gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": entry}},
	}
}

// recordParams runs the flow over the one report directory "reports", which
// reader answers for, against the checkout in dir.
func recordParams(dir string, reader recordstages.ReportReader) Params {
	return Params{
		Dir:     dir,
		Branch:  "main",
		Reports: []string{"reports"},
		Reader:  reader,
		LangEnabled: func(runner.Lang, config.Config) bool {
			return true
		},
		ScannerGates: func(runner.Lang) []string { return nil },
		RestoreToleratedDips: func(record, _ gitstate.Baseline, _ float64) gitstate.Baseline {
			return record
		},
	}
}

// recordGuarded is every stage that runs only when the recording binds to the
// checked-out tree.
var recordGuarded = []string{
	StageCountFindings, StageBindMutants, StageComposeLedgerInputs, StageComposeRecords,
	StageDecideBaseline, StageWriteState,
}

// recordRun builds the flow and runs it with p.
func recordRun(t *testing.T, p Params) (*flow.Result, error) {
	t.Helper()
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f.Run(context.Background(), p.Inputs())
}

// Every binding in the declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestTheFlowBuilds(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.Name() != Name {
		t.Errorf("Name() = %q, want %q", f.Name(), Name)
	}
}

// Params.Inputs supplies exactly the inputs the flow reads, each assignable to
// the type the flow reads it as. A key the flow reads and Params leaves out
// is a run refused before its first stage; a key Params supplies and nothing
// reads is a value the caller believes reaches a stage and does not.
func TestParamsSupplyExactlyTheInputsTheFlowReads(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := f.Inputs()
	got := (Params{}).Inputs()
	if w, g := slices.Sorted(maps.Keys(want)), slices.Sorted(maps.Keys(got)); !slices.Equal(w, g) {
		t.Fatalf("the flow reads %v, and Params supplies %v", w, g)
	}
	for key, typ := range want {
		if v := got[key]; v != nil && !reflect.TypeOf(v).AssignableTo(typ) {
			t.Errorf("input %q is %T, which the flow reads as %s", key, v, typ)
		}
	}
}

// A set of directories holding no measurements stops the run at the fold, as
// the fold's own error: there is no tree to bind anything to, so nothing past
// it is reached.
func TestNoMeasurementsStopsTheRunAtTheFold(t *testing.T) {
	res, err := recordRun(t, recordParams(t.TempDir(), &recordReader{}))

	var serr *flow.StageError
	if !errors.As(err, &serr) || serr.Stage != StageFoldMeasurements {
		t.Fatalf("err = %v, want a *flow.StageError for %q", err, StageFoldMeasurements)
	}
	if !errors.Is(err, recordstages.ErrNoMeasurements) {
		t.Errorf("err = %v, want it to wrap ErrNoMeasurements", err)
	}
	if st := res.Status(StageReadReports); st != flow.StatusSucceeded {
		t.Errorf("%s is %v, want succeeded: its rows are still the caller's to render", StageReadReports, st)
	}
	for _, stage := range append([]string{StageBindTree}, recordGuarded...) {
		if st := res.Status(stage); st != flow.StatusNotReached {
			t.Errorf("%s is %v, want not reached", stage, st)
		}
	}
}

// Measurements of another tree skip every stage past the binding, and nothing
// reaches the state branch: neither a baseline nor a record is filed against
// a checkout nobody measured. Mutant counts that were read are never even
// folded.
func TestATreeMismatchSkipsEveryGuardedStage(t *testing.T) {
	dir, origin, tree := recordRepo(t)
	const other = "0123456789abcdef0123456789abcdef01234567"
	reader := &recordReader{
		measurements: map[string]recordstages.Measurements{"reports": recordMeasured(other)},
		mutants:      map[string]recordstages.Mutants{"reports": {Tree: other}},
	}

	res, err := recordRun(t, recordParams(dir, reader))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	bound, err := flow.Output[recordstages.BindTreeOut](res, StageBindTree)
	if err != nil {
		t.Fatalf("bind-tree: %v", err)
	}
	if want := (recordstages.BindTreeOut{Bound: false, Head: tree, Measured: other}); bound != want {
		t.Errorf("bind-tree = %+v, want %+v", bound, want)
	}
	for _, stage := range recordGuarded {
		if !res.Skipped(stage) {
			t.Errorf("%s is %v, want skipped", stage, res.Status(stage))
		}
	}
	if errs := res.Errors(); len(errs) != 0 {
		t.Errorf("Errors() = %v, want none", errs)
	}
	if reader.foldedMutants != 0 {
		t.Errorf("the mutant counts were folded %d times, want never", reader.foldedMutants)
	}
	if held := recordStateBranch(t, origin); held != "" {
		t.Errorf("origin holds %q, want no %s branch", held, gitstate.BranchName)
	}
}

// A bound recording lands its baseline and its record in the one write, with
// every guarded stage run.
func TestABoundRecordingLandsTheBaselineAndTheHistory(t *testing.T) {
	dir, origin, tree := recordRepo(t)
	reader := &recordReader{
		measurements: map[string]recordstages.Measurements{"reports": recordMeasured(tree)},
	}

	res, err := recordRun(t, recordParams(dir, reader))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, stage := range recordGuarded {
		if st := res.Status(stage); st != flow.StatusSucceeded {
			t.Errorf("%s is %v, want succeeded", stage, st)
		}
	}
	baseline, err := flow.Output[recordstages.DecideBaselineOut](res, StageDecideBaseline)
	if err != nil {
		t.Fatalf("decide-baseline: %v", err)
	}
	if baseline.Verdict != recordstages.VerdictToRecord {
		t.Errorf("Verdict = %v, want VerdictToRecord", baseline.Verdict)
	}
	written, err := flow.Output[ledgerstages.WriteStateOut](res, StageWriteState)
	if err != nil {
		t.Fatalf("write-state: %v", err)
	}
	if len(written.Landed) != 1 || written.Landed[0].Branch != "main" {
		t.Errorf("Landed = %+v, want this commit's one record on main", written.Landed)
	}
	if recordStateBranch(t, origin) == "" {
		t.Fatalf("origin holds no %s branch after a recording that landed", gitstate.BranchName)
	}
	held, err := gitstate.ReadSnapshot(context.Background(), dir, tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := held.Coverage["svc"]; !ok {
		t.Errorf("the branch holds %+v, want svc's baseline", held.Coverage)
	}
}

// A write that never lands is recorded on the Result and the run still
// completes: whether it fails the recording is the caller's to judge from what
// decide-baseline and compose-records said was being landed, and both
// outcomes are still there to read.
func TestAWriteThatNeverLandsIsRecordedAndTheRunCompletes(t *testing.T) {
	dir, origin, tree := recordRepo(t)
	recordRejectPushes(t, origin)
	reader := &recordReader{
		measurements: map[string]recordstages.Measurements{"reports": recordMeasured(tree)},
	}

	res, err := recordRun(t, recordParams(dir, reader))
	if err != nil {
		t.Fatalf("Run: %v, want the run to complete", err)
	}
	if st := res.Status(StageWriteState); st != flow.StatusFailed {
		t.Errorf("%s is %v, want failed", StageWriteState, st)
	}
	errs := res.Errors()
	if len(errs) != 1 || errs[0].Stage != StageWriteState {
		t.Fatalf("Errors() = %v, want write-state's error alone", errs)
	}
	prefix := "pushing the recording for " + tree + " to " + gitstate.BranchName
	if !strings.HasPrefix(errs[0].Err.Error(), prefix) {
		t.Errorf("write-state's error = %v, want gitstate.Write's own, starting %q", errs[0].Err, prefix)
	}
	if _, err := flow.Output[ledgerstages.WriteStateOut](res, StageWriteState); !errors.Is(err, flow.ErrUnavailable) {
		t.Errorf("write-state's output: err = %v, want it unavailable", err)
	}
	history, err := flow.Output[ledgerstages.ComposeRecordsOut](res, StageComposeRecords)
	if err != nil {
		t.Fatalf("compose-records: %v", err)
	}
	if history.Reason != nil || history.Records == nil {
		t.Errorf("history reason = %v, want none and records to append", history.Reason)
	}
	baseline, err := flow.Output[recordstages.DecideBaselineOut](res, StageDecideBaseline)
	if err != nil {
		t.Fatalf("decide-baseline: %v", err)
	}
	if !baseline.Snapshot.Recorded() {
		t.Error("decide-baseline's snapshot is empty, want the baseline that was being landed")
	}
	if held := recordStateBranch(t, origin); held != "" {
		t.Errorf("origin holds %q after every push was rejected", held)
	}
}
