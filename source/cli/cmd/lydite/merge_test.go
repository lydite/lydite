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
		&measurementsDoc{Tree: "tree", Components: map[string]componentMeasurement{
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
