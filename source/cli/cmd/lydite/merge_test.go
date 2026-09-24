package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/ui"
)

// mergeRepo declares two components, which is the smallest declaration that
// can be sharded and the smallest one a gap in the fold is visible in.
func mergeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: a\n    dir: moda\n    runner: go-test\n"+
			"  - name: b\n    dir: modb\n    runner: go-test\n")
	write(t, root, "moda/.keep", "")
	write(t, root, "modb/.keep", "")
	return root
}

// shardDir writes one shard's report directory: the document it rendered, and
// the measurements it took.
func shardDir(t *testing.T, rows []ui.Row, doc *measurementsDoc) string {
	t.Helper()
	dir := t.TempDir()
	rep := ui.NewReport("test")
	for _, r := range rows {
		rep.Add(r)
	}
	f, err := os.Create(filepath.Join(dir, documentName("test"))) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if doc != nil {
		data, err := json.Marshal(*doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, measurementsName), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ungatedFlaky is the flaky row a shard writes for a component whose new tests
// it was not asked to rerun.
//
// Every shard writes one per component it is responsible for, gated or not, so
// the fold holds them against the declaration the way it holds the suite rows:
// a component with no flaky row anywhere is a shard whose job died.
func ungatedFlaky(name string) ui.Row {
	return ui.Row{Status: ui.StatusContext, Label: "flaky(" + name + ")",
		Value: "not gated — --gate-flaky reruns the tests a change introduces"}
}

func runMergeCmd(t *testing.T, root string, reports ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	args := []string{"test", "merge", "--dir", root, "--no-color", "--json"}
	for _, r := range reports {
		args = append(args, "--reports", r)
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// shardOf is one shard's rows and measurements for component name, as a run
// responsible for it alone writes them.
func shardOf(t *testing.T, name string, covered, total int) string {
	t.Helper()
	return shardDir(t,
		[]ui.Row{
			{Status: ui.StatusPass, Label: "orphans", Value: "none in 2 source file(s)"},
			{Status: ui.StatusPass, Label: "watch", Value: "none declared"},
			{Status: ui.StatusPass, Label: "schedule", Value: "1 component(s), max 1 concurrent"},
			{Status: ui.StatusPass, Label: "test(" + name + ")", Value: "passed"},
			ungatedFlaky(name),
			{Status: ui.StatusPass, Label: "coverage(" + name + ")", Value: "measured"},
		},
		&measurementsDoc{Tree: "tree", Gated: true, Components: map[string]componentMeasurement{
			name: {
				Entry: gitstate.Entry{LineCount: coverage.LineCount{Covered: covered, Total: total}, Producer: "go"},
				Base:  &gitstate.Entry{LineCount: coverage.LineCount{Covered: covered, Total: total}, Producer: "go"},
			},
		}})
}

// The figure over the repository is the one row no shard can produce: each
// sums its own components, so three shards would publish three answers to a
// question about the repository.
func TestMergeComposesTheRepositoryFigureOnce(t *testing.T) {
	root := mergeRepo(t)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), shardOf(t, "b", 3, 4))
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, repoLabel("coverage"))
	if got.Status != "pass" || !strings.Contains(got.Value, "baseline") {
		t.Errorf("coverage(repo) = %+v, want a gated row: the shards compared against a baseline", got)
	}
	if !strings.HasPrefix(got.Value, "66.7% (4/6 lines), 2 of 2 component(s)") {
		t.Errorf("coverage(repo) = %q, want the sum of both shards' counts", got.Value)
	}
	if strings.Count(out, `"coverage(repo)"`) != 1 {
		t.Errorf("coverage(repo) appears %d times", strings.Count(out, `"coverage(repo)"`))
	}
	if got := jsonRowByLabel(t, out, "shards"); got.Status != "pass" {
		t.Errorf("shards = %+v, want a pass", got)
	}
}

// A shard whose job died leaves its components with no row at all. An
// unmeasured row does not vote, so reporting one would publish
// `"verdict": "pass"` over a repository half of which was never tested.
func TestMergeFailsWhenAComponentHasNoRow(t *testing.T) {
	root := mergeRepo(t)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2))
	if err == nil {
		t.Fatalf("a missing shard folded cleanly:\n%s", out)
	}
	got := jsonRowByLabel(t, out, "shards")
	if got.Status != "fail" {
		t.Errorf("shards = %+v, want a failure", got)
	}
	if !strings.Contains(strings.Join(got.Detail, "\n"), "b has no row") {
		t.Errorf("shards detail %q does not name the absent component", got.Detail)
	}
}

// Two rows under one label means two jobs ran the same suite, and a consumer
// keying rows by label picks one of two answers.
func TestMergeFailsWhenAComponentHasTwoRows(t *testing.T) {
	root := mergeRepo(t)
	both := shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: "test(a)", Value: "passed"},
		ungatedFlaky("a"),
		{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
		ungatedFlaky("b"),
	}, nil)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), both)
	if err == nil {
		t.Fatalf("a duplicated component folded cleanly:\n%s", out)
	}
	if got := jsonRowByLabel(t, out, "shards"); !strings.Contains(strings.Join(got.Detail, "\n"), "a has a row in 2") {
		t.Errorf("shards detail %q does not name the duplicate", got.Detail)
	}
}

// A flaky row folds the way a suite row does, because it is the same shape:
// exactly one per declared component across the shards. A component with none
// is a shard whose job died, and one with two is two jobs that reran the same
// new tests.
func TestMergeFoldsTheFlakyRowsLikeTheSuiteRows(t *testing.T) {
	root := mergeRepo(t)
	gated := func(name string) ui.Row {
		return ui.Row{Status: ui.StatusPass, Label: "flaky(" + name + ")", Value: "2 new test(s), 2 runs each"}
	}
	shard := func(rows ...ui.Row) string { return shardDir(t, rows, nil) }

	// One shard's flaky row is missing while its suite row is not, which is
	// exactly the gap a fold that only counted suite rows would pass.
	out, err := runMergeCmd(t, root,
		shard(ui.Row{Status: ui.StatusPass, Label: "test(a)", Value: "passed"}, gated("a")),
		shard(ui.Row{Status: ui.StatusPass, Label: "test(b)", Value: "passed"}))
	if err == nil {
		t.Fatalf("a component with no flaky row folded cleanly:\n%s", out)
	}
	if detail := strings.Join(jsonRowByLabel(t, out, "shards").Detail, "\n"); !strings.Contains(detail, "flaky: b has no row") {
		t.Errorf("shards detail %q does not name the gate whose row is missing", detail)
	}

	// Two shards claiming one component's new tests is two jobs running the
	// same rerun, and a consumer keying rows by label picks one of two answers.
	out, err = runMergeCmd(t, root,
		shard(ui.Row{Status: ui.StatusPass, Label: "test(a)", Value: "passed"}, gated("a")),
		shard(ui.Row{Status: ui.StatusPass, Label: "test(b)", Value: "passed"}, gated("b"), gated("a")))
	if err == nil {
		t.Fatalf("a duplicated flaky row folded cleanly:\n%s", out)
	}
	if detail := strings.Join(jsonRowByLabel(t, out, "shards").Detail, "\n"); !strings.Contains(detail, "flaky: a has a row in 2") {
		t.Errorf("shards detail %q does not name the duplicate", detail)
	}

	// And a complete matrix carries each shard's own row, once.
	out, err = runMergeCmd(t, root,
		shard(ui.Row{Status: ui.StatusPass, Label: "test(a)", Value: "passed"}, gated("a")),
		shard(ui.Row{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
			ui.Row{Status: ui.StatusFail, Label: "flaky(b)", Value: "1 of 2 new test(s) disagreed between two runs"}))
	if err == nil {
		t.Fatalf("a shard whose flaky gate failed folded to a pass:\n%s", out)
	}
	if got := jsonRowByLabel(t, out, "flaky(b)"); got.Status != "fail" {
		t.Errorf("flaky(b) = %+v, want the shard's own failure carried", got)
	}
	if n := strings.Count(out, `"flaky(a)"`); n != 1 {
		t.Errorf("flaky(a) appears %d times", n)
	}
}

// The whole-tree gates ask about the declaration and the tree, so every shard
// computes the same answer. Two that differ did not see the same tree, which
// makes every other row in the fold suspect.
func TestMergeFailsWhenTheWholeTreeGatesDisagree(t *testing.T) {
	root := mergeRepo(t)
	odd := shardDir(t, []ui.Row{
		{Status: ui.StatusFail, Label: "orphans", Value: "1 under no component"},
		{Status: ui.StatusPass, Label: "watch", Value: "none declared"},
		{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
		ungatedFlaky("b"),
	}, nil)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), odd)
	if err == nil {
		t.Fatalf("shards that saw different trees folded cleanly:\n%s", out)
	}
	if got := jsonRowByLabel(t, out, "shards"); !strings.Contains(strings.Join(got.Detail, "\n"), "disagree about orphans") {
		t.Errorf("shards detail %q does not name the disagreement", got.Detail)
	}
}

// A shard run with --no-coverage writes no measurements at all. That is a run
// that measured nothing rather than a shard that went missing, so the fold
// says nothing about coverage instead of failing.
func TestMergeToleratesAShardThatMeasuredNothing(t *testing.T) {
	root := mergeRepo(t)
	bare := shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
		ungatedFlaky("b"),
	}, nil)
	plain := shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: "test(a)", Value: "passed"},
		ungatedFlaky("a"),
	}, nil)
	out, err := runMergeCmd(t, root, plain, bare)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	if strings.Contains(out, `"coverage(repo)"`) {
		t.Errorf("a run that measured nothing published a figure about the repository:\n%s", out)
	}
}

// The shards agreed about what they measured and one of them still failed. A
// fold that reproduced no failing row would turn a red matrix green.
func TestMergeCarriesAShardsVerdict(t *testing.T) {
	root := mergeRepo(t)
	failing := shardDir(t, []ui.Row{
		{Status: ui.StatusFail, Label: "test(b)", Value: "failed"},
		ungatedFlaky("b"),
	}, nil)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), failing)
	if err == nil {
		t.Fatalf("a failing shard folded to a pass:\n%s", out)
	}
	if got := jsonRowByLabel(t, out, "test(b)"); got.Status != "fail" {
		t.Errorf("test(b) = %+v, want the shard's own failure", got)
	}
}

// A directory the matrix was supposed to fill and did not is a job that never
// uploaded, which is the same thing as a shard that died.
func TestMergeFailsOnADirectoryWithNoReport(t *testing.T) {
	root := mergeRepo(t)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), t.TempDir())
	if err == nil {
		t.Fatalf("an empty report directory folded cleanly:\n%s", out)
	}
	if !strings.Contains(out, `"no test report"`) {
		t.Errorf("the fold does not name the directory it could not read:\n%s", out)
	}
}

// A measurements file that is there and will not parse is neither a shard that
// gated nothing nor one that went missing. Treated as absent it would leave
// that shard's components composing nothing while its `read` row still said
// the document was fine.
func TestMergeNamesMeasurementsItCannotRead(t *testing.T) {
	root := mergeRepo(t)
	broken := shardOf(t, "b", 3, 4)
	if err := os.WriteFile(filepath.Join(broken, measurementsName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), broken)
	if err == nil {
		t.Fatalf("an unreadable measurements document folded cleanly:\n%s", out)
	}
	if !strings.Contains(out, "measurements not readable") {
		t.Errorf("the fold does not name the document it could not read:\n%s", out)
	}
}

// A carried entry describes the base tree, and the component it belongs to ran
// in no shard. Counted as measured it fails a floor no shard emitted a row for
// — and floorSummaryRow suppresses the summary when anything is below, so the
// merged report would carry no floor row at all with a floor configured and a
// component under it.
func TestMergeHoldsTheFloorAgainstWhatTheShardsMeasured(t *testing.T) {
	root := mergeRepo(t)
	write(t, root, ".lydite/config.yml", "coverage:\n  floor: 80\n")
	carried := shardDir(t,
		[]ui.Row{
			{Status: ui.StatusUnmeasured, Label: "test(b)", Value: "not affected"},
			ungatedFlaky("b"),
			{Status: ui.StatusUnmeasured, Label: "coverage(b)", Value: "not measured — the component was not selected for this run"},
		},
		&measurementsDoc{Tree: "tree", Components: map[string]componentMeasurement{
			// Well below the floor, and carried: the shard that owns b never
			// ran it, so no floor(b) row exists anywhere.
			"b": {Entry: gitstate.Entry{LineCount: coverage.LineCount{Covered: 1, Total: 10}, Producer: "go"}, Carried: true},
		}})
	out, err := runMergeCmd(t, root, shardOf(t, "a", 9, 10), carried)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "floor")
	if got.Status != "pass" || !strings.HasPrefix(got.Value, "1 of 2 component(s)") {
		t.Errorf("floor = %+v, want a pass counting only what the shards measured", got)
	}
}

// A run where HEAD is its own merge-base measures every component and holds
// none of them to anything, and its rows say so. A fold that gated anyway
// would publish a verdict the run it folded refused to reach, against a
// baseline no shard was ever held to.
func TestMergeDoesNotGateWhatTheShardsDidNot(t *testing.T) {
	root := mergeRepo(t)
	ungated := func(name string, covered, total int) string {
		return shardDir(t,
			[]ui.Row{
				{Status: ui.StatusPass, Label: "test(" + name + ")", Value: "passed"},
				ungatedFlaky(name),
				{Status: ui.StatusContext, Label: "coverage(" + name + ")", Value: "measured"},
			},
			&measurementsDoc{Tree: "tree", Components: map[string]componentMeasurement{
				name: {Entry: gitstate.Entry{
					LineCount: coverage.LineCount{Covered: covered, Total: total}, Producer: "go"}},
			}})
	}
	out, err := runMergeCmd(t, root, ungated("a", 1, 2), ungated("b", 3, 4))
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, repoLabel("coverage"))
	if got.Status != "context" {
		t.Errorf("coverage(repo) = %+v, want a context row: no shard compared anything", got)
	}
	if strings.Contains(got.Value, "baseline") {
		t.Errorf("coverage(repo) = %q, want no comparison", got.Value)
	}
}

// A configured floor with nothing to hold anything to must not read as a
// repository that cleared it. A shard emits no summary — the count is over the
// whole declaration — so without a row here the merged report is
// byte-identical to one where the floor is off.
func TestMergeSaysAFloorItCouldNotApply(t *testing.T) {
	root := mergeRepo(t)
	write(t, root, ".lydite/config.yml", "coverage:\n  floor: 80\n")
	plain := shardDir(t, []ui.Row{{Status: ui.StatusPass, Label: "test(a)", Value: "passed"}, ungatedFlaky("a")}, nil)
	bare := shardDir(t, []ui.Row{{Status: ui.StatusPass, Label: "test(b)", Value: "passed"}, ungatedFlaky("b")}, nil)

	out, err := runMergeCmd(t, root, plain, bare)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "floor")
	if got.Status != "unmeasured" {
		t.Errorf("floor = %+v, want unmeasured: nothing was held to it", got)
	}
}

// The fold's schedule row carries two things a single shard's cannot: whether
// any shard was cut short, and the largest number of components any one of them
// ran at once. The second is what separates a scheduler that ran from one that
// only claims to, and neither can be recomputed — both are observed.
func TestMergeFoldsWhatTheSchedulersDid(t *testing.T) {
	root := mergeRepo(t)
	busy := shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: "schedule", Value: "2 component(s), max 2 concurrent, 1 pair(s) serialised",
			Detail: []string{"a and b serialised on port 5432"}},
		{Status: ui.StatusPass, Label: "test(a)", Value: "passed"},
		ungatedFlaky("a"),
	}, nil)
	cut := shardDir(t, []ui.Row{
		{Status: ui.StatusFail, Label: "schedule", Value: "interrupted after 0 of 1 component(s)"},
		{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
		ungatedFlaky("b"),
	}, nil)

	out, err := runMergeCmd(t, root, busy, cut)
	if err == nil {
		t.Fatalf("a shard whose run was cut short folded to a pass:\n%s", out)
	}
	got := jsonRowByLabel(t, out, "schedule")
	if got.Status != "fail" {
		t.Errorf("schedule = %+v, want the interrupted shard's failure carried", got)
	}
	if !strings.Contains(got.Value, "max 2 concurrent") {
		t.Errorf("schedule = %q, want the highest concurrency any shard reached", got.Value)
	}
	// Every shard the fold read, not only those that scheduled something: a
	// shard whose components were all deselected ran no scheduler and still
	// folded into this run.
	if !strings.Contains(got.Value, "2 shard(s)") {
		t.Errorf("schedule = %q, want the number of shards folded", got.Value)
	}
	if !strings.Contains(strings.Join(got.Detail, "\n"), "serialised on port 5432") {
		t.Errorf("schedule detail %q does not carry the shard's serialised pair", got.Detail)
	}
}

// A run where no component was selected schedules nothing, and emits no
// schedule row. A fold inventing a green one would be more assertive about the
// scheduler than the runs it folds.
func TestMergeInventsNoScheduleRow(t *testing.T) {
	root := mergeRepo(t)
	idle := func(name string) string {
		return shardDir(t, []ui.Row{
			{Status: ui.StatusUnmeasured, Label: "test(" + name + ")", Value: "not affected"},
			ungatedFlaky(name),
		}, nil)
	}
	out, err := runMergeCmd(t, root, idle("a"), idle("b"))
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	if strings.Contains(out, `"schedule"`) {
		t.Errorf("the fold invented a schedule row for a run that scheduled nothing:\n%s", out)
	}
}

// The shards measured and were never asked to gate. The figure over the
// repository is composed from counts rather than from rendered rows, so it
// cannot be recovered here — and a fold that silently lost the row a single
// process publishes is indistinguishable from a repository nobody measured.
func TestMergeSaysItLostTheFigureItCannotCompose(t *testing.T) {
	root := mergeRepo(t)
	measuredOnly := func(name string) string {
		return shardDir(t, []ui.Row{
			{Status: ui.StatusPass, Label: "test(" + name + ")", Value: "passed"},
			ungatedFlaky(name),
			{Status: ui.StatusContext, Label: "coverage(" + name + ")", Value: "50.0% (1/2 lines)"},
		}, nil)
	}
	out, err := runMergeCmd(t, root, measuredOnly("a"), measuredOnly("b"))
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, repoLabel("coverage"))
	if got.Status != "unmeasured" || !strings.Contains(got.Value, measurementsName) {
		t.Errorf("coverage(repo) = %+v, want an unmeasured row naming what the shards did not write", got)
	}
}

// Every shard measured nothing and said why. The fold has a tree and no counts,
// which is a run with nothing to record rather than one whose shards went
// missing.
func TestMergeReportsAFoldWithNothingToRecord(t *testing.T) {
	root := mergeRepo(t)
	barren := func(name string) string {
		return shardDir(t,
			[]ui.Row{{Status: ui.StatusPass, Label: "test(" + name + ")", Value: "passed"}, ungatedFlaky(name)},
			&measurementsDoc{Tree: "tree", Gated: true, Reason: "no component produced a measurement"})
	}
	out, err := runMergeCmd(t, root, barren("a"), barren("b"))
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "record")
	if got.Status != "unmeasured" || got.Value != "nothing to record" {
		t.Errorf("record = %+v, want an unmeasured row saying there is nothing to land", got)
	}
	if !strings.Contains(strings.Join(got.Detail, "\n"), "no component produced a measurement") {
		t.Errorf("record detail %q does not carry the shards' reason", got.Detail)
	}
}

// The fold is the only thing that can hold a candidate against the whole
// declaration, so it must not announce a recording `lydite test record` will
// then refuse.
func TestMergeDoesNotPromiseARecordingThatWillBeRefused(t *testing.T) {
	root := mergeRepo(t)
	partial := shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
		ungatedFlaky("b"),
		// b ran and its report was unreadable, so it contributes no entry.
		{Status: ui.StatusUnmeasured, Label: "coverage(b)", Value: "not measured — the coverage report lists no coverable line"},
	}, &measurementsDoc{Tree: "tree", Gated: true, Components: map[string]componentMeasurement{}})

	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), partial)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "record")
	if got.Status == "context" || !strings.Contains(got.Value, "b") {
		t.Errorf("record = %+v, want a non-passing row naming the component with no entry", got)
	}
}

// The fold is the only thing that can say a candidate covers the whole
// declaration, so when it does the row says so against that denominator — the
// two numbers differ exactly when `lydite test record` has something left to
// refuse.
func TestMergeReportsACompleteCandidateAgainstTheDeclaration(t *testing.T) {
	root := mergeRepo(t)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), shardOf(t, "b", 3, 4))
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "record")
	if got.Status != "context" {
		t.Errorf("record = %+v, want a context row: the fold is complete", got)
	}
	if !strings.HasPrefix(got.Value, "2 of 2 component(s) ready") {
		t.Errorf("record = %q, want the count against the declaration", got.Value)
	}
}

// A fold over no component cannot report a shard that died: completeness is a
// question about the declaration, and an empty one answers every question with
// yes. `plan` and `scan` refuse the same state.
func TestMergeRefusesADeclarationWithNoComponents(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components: []\n")
	out, err := runMergeCmd(t, root, shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: "orphans", Value: "none in 0 source file(s)"},
	}, nil))
	if err == nil {
		t.Fatalf("a fold over no component reported a verdict:\n%s", out)
	}
	if !strings.Contains(err.Error(), "nothing to fold") {
		t.Errorf("error = %v, want it to say there is nothing to fold", err)
	}
}

// A row the fold has no rule for is carried through, once when the shards
// agree about it and once per shard when they do not. Without that a shard's
// own row disappears into a fold that reproduces everything else, which is a
// run saying less than the runs it folds.
func TestATestRowTheFoldHasNoRuleForIsCarried(t *testing.T) {
	root := mergeRepo(t)
	odd := ui.Row{Status: ui.StatusContext, Label: "toolchain", Value: "go 1.26.6"}
	out, err := runMergeCmd(t, root, withRows(t, shardOf(t, "a", 5, 10), odd), withRows(t, shardOf(t, "b", 5, 10), odd))
	if err != nil {
		t.Fatalf("a complete fold failed: %v\n%s", err, out)
	}
	if got := countRows(t, out, "toolchain"); got != 1 {
		t.Errorf("a row the shards agreed on appears %d time(s), want once", got)
	}

	out, _ = runMergeCmd(t, root, withRows(t, shardOf(t, "a", 5, 10), odd),
		withRows(t, shardOf(t, "b", 5, 10), ui.Row{Status: ui.StatusContext, Label: "toolchain", Value: "go 1.25.0"}))
	if got := countRows(t, out, "toolchain"); got != 2 {
		t.Errorf("the shards disagreed and the fold kept %d row(s), want both", got)
	}
}

// A whole-tree gate no shard wrote is a gate the fold has nothing to say
// about, and an empty row under its label is a gate that did not run reading
// as one that did.
func TestAWholeTreeGateNoShardWroteTakesNoRow(t *testing.T) {
	root := mergeRepo(t)
	out, err := runMergeCmd(t, root, withoutRow(t, shardOf(t, "a", 5, 10), "orphans"),
		withoutRow(t, shardOf(t, "b", 5, 10), "orphans"))
	if err != nil {
		t.Fatalf("a complete fold failed: %v\n%s", err, out)
	}
	if got := countRows(t, out, "orphans"); got != 0 {
		t.Errorf("the fold emitted %d orphans row(s) over shards that wrote none", got)
	}
	// The gate every shard did write still folds to exactly one.
	if got := countRows(t, out, "watch"); got != 1 {
		t.Errorf("the watch row appears %d time(s), want once", got)
	}
}

// No shard scheduled anything — every component was deselected. A single run
// emits no schedule row in that state, and a fold inventing a green one would
// be more assertive about the scheduler than the runs it folds.
func TestAFoldOverShardsThatScheduledNothingEmitsNoScheduleRow(t *testing.T) {
	root := mergeRepo(t)
	out, err := runMergeCmd(t, root, withoutRow(t, shardOf(t, "a", 5, 10), "schedule"),
		withoutRow(t, shardOf(t, "b", 5, 10), "schedule"))
	if err != nil {
		t.Fatalf("a complete fold failed: %v\n%s", err, out)
	}
	if got := countRows(t, out, "schedule"); got != 0 {
		t.Errorf("the fold invented %d schedule row(s) over shards that scheduled nothing", got)
	}
}

// The safety net under the reasoning that every shard's row is reproduced or
// replaced. A shard that failed over something the fold carries no row for
// must not fold into a pass — and this is the only thing that would notice.
func TestAShardThatFailedOverNothingTheFoldCarriesIsReported(t *testing.T) {
	root := mergeRepo(t)
	failing := shardOf(t, "a", 5, 10)
	setVerdict(t, failing, ui.VerdictFail)
	out, err := runMergeCmd(t, root, failing, shardOf(t, "b", 5, 10))
	if err == nil {
		t.Fatalf("the fold passed over a shard that failed:\n%s", out)
	}
	if !strings.Contains(out, "reproduced no row explaining it") {
		t.Errorf("the fold does not say which shard failed over nothing it carries:\n%s", out)
	}
}

// withRows rewrites a shard's document with extra rows appended.
func withRows(t *testing.T, dir string, extra ...ui.Row) string {
	t.Helper()
	doc := shardDoc(t, dir)
	doc.Rows = append(doc.Rows, extra...)
	writeShardDoc(t, dir, doc)
	return dir
}

// withoutRow rewrites a shard's document with every row under label dropped.
func withoutRow(t *testing.T, dir, label string) string {
	t.Helper()
	doc := shardDoc(t, dir)
	var kept []ui.Row
	for _, r := range doc.Rows {
		if r.Label != label {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(doc.Rows) {
		t.Fatalf("the shard wrote no %s row, so dropping it proves nothing", label)
	}
	doc.Rows = kept
	writeShardDoc(t, dir, doc)
	return dir
}

// setVerdict rewrites a shard's verdict without touching its rows, which is
// the one state a report cannot render itself into.
func setVerdict(t *testing.T, dir string, v ui.Verdict) {
	t.Helper()
	doc := shardDoc(t, dir)
	doc.Verdict = v
	writeShardDoc(t, dir, doc)
}

func shardDoc(t *testing.T, dir string) ui.Document {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, documentName("test"))) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	doc, err := ui.ReadDocument(f)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func writeShardDoc(t *testing.T, dir string, doc ui.Document) {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, documentName("test")), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// countRows counts the rows under one label in a rendered --json fold.
func countRows(t *testing.T, out, label string) int {
	t.Helper()
	var doc ui.Document
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the fold emitted no document: %v\n%s", err, out)
	}
	n := 0
	for _, r := range doc.Rows {
		if r.Label == label {
			n++
		}
	}
	return n
}

// The fold carries each shard's own crap row and emits the figure over the
// repository, which is the two ledger scalars summed. A shard cannot emit it:
// each sums its own components, so two shards would publish two answers to a
// question about the repository — the reason coverage(repo) belongs here too.
//
// It is composed from the shards' scalars and not from their rendered rows: a
// report's rows carry prose, so folding them could not recover a number.
func TestMergeSumsTheScoresNoShardCanAnswerFor(t *testing.T) {
	root := mergeRepo(t)
	shard := func(name string, above int, worst float64) string {
		t.Helper()
		return shardDir(t,
			[]ui.Row{
				{Status: ui.StatusPass, Label: "orphans", Value: "none in 2 source file(s)"},
				{Status: ui.StatusPass, Label: "watch", Value: "none declared"},
				{Status: ui.StatusPass, Label: "schedule", Value: "1 component(s), max 1 concurrent"},
				{Status: ui.StatusPass, Label: "test(" + name + ")", Value: "passed"},
				ungatedFlaky(name),
				{Status: ui.StatusPass, Label: "coverage(" + name + ")", Value: "measured"},
				{Status: ui.StatusPass, Label: "crap(" + name + ")",
					Value: fmt.Sprintf("%d function(s) above 30, worst %.1f, baseline %d", above, worst, above)},
			},
			&measurementsDoc{Tree: "tree", Gated: true, Components: map[string]componentMeasurement{
				name: {
					Entry: gitstate.Entry{LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "go"},
					Base:  &gitstate.Entry{LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "go"},
					CRAP:  &gitstate.CRAPEntry{Above: above, Worst: worst, Producer: "go"},
				},
			}})
	}
	// One shard measured its component; the other carried its score forward,
	// which is what an --affected matrix produces. Both count towards the
	// figure and the carried one is named, so the fold and an unsharded run
	// answer the same tree the same way.
	carried := shardDir(t,
		[]ui.Row{
			{Status: ui.StatusPass, Label: "orphans", Value: "none in 2 source file(s)"},
			{Status: ui.StatusPass, Label: "watch", Value: "none declared"},
			{Status: ui.StatusPass, Label: "schedule", Value: "1 component(s), max 1 concurrent"},
			{Status: ui.StatusUnmeasured, Label: "test(b)", Value: "not measured — the component was not selected for this run"},
			ungatedFlaky("b"),
			{Status: ui.StatusUnmeasured, Label: "coverage(b)", Value: "not measured — the component was not selected for this run"},
			{Status: ui.StatusUnmeasured, Label: "crap(b)", Value: "not measured — the component was not selected for this run"},
		},
		&measurementsDoc{Tree: "tree", Gated: true, Components: map[string]componentMeasurement{
			"b": {
				Entry:   gitstate.Entry{LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "go"},
				Carried: true,
				CRAP:    &gitstate.CRAPEntry{Above: 2, Worst: 156.3, Producer: "go"},
			},
		}})
	withCarried, err := runMergeCmd(t, root, shard("a", 3, 41.5), carried)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, withCarried)
	}
	if got := jsonRowByLabel(t, withCarried, "crap").Value; !strings.Contains(got, "2 of 2 component(s), 1 carried forward") {
		t.Errorf("crap = %q, want the carried component counted and named", got)
	}

	out, err := runMergeCmd(t, root, shard("a", 3, 41.5), shard("b", 2, 156.3))
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	// Each shard's own row survives, exactly once.
	for _, label := range []string{"crap(a)", "crap(b)"} {
		if got := jsonRowByLabel(t, out, label); got.Status != "pass" {
			t.Errorf("%s = %+v, want the shard's own row carried", label, got)
		}
		if n := strings.Count(out, `"`+label+`"`); n != 1 {
			t.Errorf("%s appears %d times", label, n)
		}
	}
	got := jsonRowByLabel(t, out, "crap")
	if got.Status != "context" {
		t.Errorf("crap = %+v, want a context row — every component's own row carries the gate", got)
	}
	if !strings.Contains(got.Value, "5 function(s) above 30 across 2 of 2 component(s)") {
		t.Errorf("crap = %q, want both shards' counts summed", got.Value)
	}
	if !strings.Contains(got.Value, "worst 156.3") {
		t.Errorf("crap = %q, want the worst across the repository", got.Value)
	}
}

// The fold's denominator is a property of the declaration, counted before any
// document is folded — so a Rust or TypeScript component counts toward it the
// same way a Go one does, even when no shard's measurements hold it at all.
// An unsharded run over the same declaration would count it too; a fold that
// disagreed would swing lower every time a language other than Go went
// unmeasured, which is not a property of sharding.
func TestMergeCountsARustComponentTowardTheDenominatorEvenUnmeasured(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: svc\n    dir: svc\n    runner: go-test\n"+
			"  - name: web\n    dir: web\n    runner: cargo-nextest\n")
	write(t, root, "svc/.keep", "")
	write(t, root, "web/.keep", "")

	shard := shardDir(t,
		[]ui.Row{
			{Status: ui.StatusPass, Label: "orphans", Value: "none in 2 source file(s)"},
			{Status: ui.StatusPass, Label: "watch", Value: "none declared"},
			{Status: ui.StatusPass, Label: "schedule", Value: "2 component(s), max 1 concurrent"},
			{Status: ui.StatusPass, Label: "test(svc)", Value: "passed"},
			ungatedFlaky("svc"),
			{Status: ui.StatusPass, Label: "coverage(svc)", Value: "measured"},
			{Status: ui.StatusPass, Label: "crap(svc)", Value: "3 function(s) above 30, worst 41.5, baseline 3"},
			{Status: ui.StatusPass, Label: "test(web)", Value: "passed"},
			ungatedFlaky("web"),
			{Status: ui.StatusUnmeasured, Label: "coverage(web)", Value: "not measured — no shard's measurements hold this component"},
			{Status: ui.StatusUnmeasured, Label: "crap(web)", Value: "not measured — no shard's measurements hold this component"},
		},
		// web is declared but absent from every document's Components map — the
		// case the fold's declaration-based count exists for.
		&measurementsDoc{Tree: "tree", Gated: true, Components: map[string]componentMeasurement{
			"svc": {
				Entry: gitstate.Entry{LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "go"},
				Base:  &gitstate.Entry{LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "go"},
				CRAP:  &gitstate.CRAPEntry{Above: 3, Worst: 41.5, Producer: "go"},
			},
		}})

	out, err := runMergeCmd(t, root, shard)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "crap")
	if !strings.Contains(got.Value, "1 of 2 component(s)") {
		t.Errorf("crap = %q, want the Rust component counted toward the denominator alongside the Go one", got.Value)
	}
}

// A component declaring no suite is in no shard, so no shard reports it — and
// it is neither a shard that died nor absent from the fold. Its rows come from
// the declaration exactly once: not zero times, and not once per shard, even
// where a shard named it with --component and reported it anyway.
func TestMergeReportsANoSuiteComponentFromTheDeclarationOnce(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: a\n    dir: moda\n    runner: go-test\n"+
			"  - name: scripts\n    dir: scripts\n    lang: shell\n"+
			"  - name: b\n    dir: modb\n    runner: go-test\n")
	for _, dir := range []string{"moda", "modb", "scripts"} {
		write(t, root, dir+"/.keep", "")
	}
	stray := withRows(t, shardOf(t, "b", 3, 4),
		ui.Row{Status: ui.StatusUnmeasured, Label: testLabel("scripts"), Value: "not run — " + noSuiteReason},
		noSuiteFlakyRow("scripts"))
	for name, shards := range map[string][]string{
		"neither shard ran it": {shardOf(t, "a", 1, 2), shardOf(t, "b", 3, 4)},
		"a shard reported it":  {shardOf(t, "a", 1, 2), stray},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runMergeCmd(t, root, shards...)
			if err != nil {
				t.Fatalf("merge: %v\n%s", err, out)
			}
			for _, label := range []string{testLabel("scripts"), flakyLabel("scripts"), "coverage(scripts)", "crap(scripts)"} {
				if n := countRows(t, out, label); n != 1 {
					t.Errorf("%s appears %d times, want exactly once", label, n)
					continue
				}
				row := jsonRowByLabel(t, out, label)
				if row.Status != string(ui.StatusUnmeasured) || !strings.Contains(row.Value, noSuiteReason) {
					t.Errorf("%s = %+v, want unmeasured, naming that it declares no suite", label, row)
				}
			}
			if got := jsonRowByLabel(t, out, "shards"); got.Status != "pass" {
				t.Errorf("shards = %+v, want a pass: no shard was meant to run it", got)
			}
			if got := jsonRowByLabel(t, out, repoLabel("coverage")); !strings.Contains(got.Value, "2 of 2 component(s)") {
				t.Errorf("coverage(repo) = %q, want a denominator counting only what a suite could measure", got.Value)
			}
		})
	}
}

// A fold over shards that measured no coverage holds no coverage row for a
// component declaring no suite either, as the single run that did not
// instrument would not.
func TestMergeGivesANoSuiteComponentNoCoverageRowsTheShardsDidNotTake(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: a\n    dir: moda\n    runner: go-test\n"+
			"  - name: scripts\n    dir: scripts\n    lang: shell\n")
	for _, dir := range []string{"moda", "scripts"} {
		write(t, root, dir+"/.keep", "")
	}
	shard := shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: testLabel("a"), Value: "passed"},
		ungatedFlaky("a"),
	}, nil)
	out, err := runMergeCmd(t, root, shard)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	if n := countRows(t, out, "coverage(scripts)"); n != 0 {
		t.Errorf("coverage(scripts) appears %d times, want none: no shard instrumented", n)
	}
	if n := countRows(t, out, testLabel("scripts")); n != 1 {
		t.Errorf("test(scripts) appears %d times, want exactly once", n)
	}
}
