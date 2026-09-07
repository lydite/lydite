package main

import (
	"bytes"
	"context"
	"encoding/json"
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
		{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
	}, nil)
	out, err := runMergeCmd(t, root, shardOf(t, "a", 1, 2), both)
	if err == nil {
		t.Fatalf("a duplicated component folded cleanly:\n%s", out)
	}
	if got := jsonRowByLabel(t, out, "shards"); !strings.Contains(strings.Join(got.Detail, "\n"), "a has a row in 2") {
		t.Errorf("shards detail %q does not name the duplicate", got.Detail)
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
	}, nil)
	plain := shardDir(t, []ui.Row{
		{Status: ui.StatusPass, Label: "test(a)", Value: "passed"},
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
	plain := shardDir(t, []ui.Row{{Status: ui.StatusPass, Label: "test(a)", Value: "passed"}}, nil)
	bare := shardDir(t, []ui.Row{{Status: ui.StatusPass, Label: "test(b)", Value: "passed"}}, nil)

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
	}, nil)
	cut := shardDir(t, []ui.Row{
		{Status: ui.StatusFail, Label: "schedule", Value: "interrupted after 0 of 1 component(s)"},
		{Status: ui.StatusPass, Label: "test(b)", Value: "passed"},
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
			[]ui.Row{{Status: ui.StatusPass, Label: "test(" + name + ")", Value: "passed"}},
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
