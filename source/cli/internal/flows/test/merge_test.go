package testflow

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/flow"
	teststages "lydite/lydite/internal/stages/test"
	"lydite/lydite/internal/ui"
)

// measurementsReader is a teststages.MeasurementsReader answering from a
// fixed per-directory table. A directory the table does not name holds no
// document at all.
type measurementsReader struct {
	errs map[string]error
}

func (r *measurementsReader) ReadMeasurements(dir string) error {
	if err, ok := r.errs[dir]; ok {
		return err
	}
	return &fs.PathError{Op: "open", Path: dir + "/measurements.json", Err: fs.ErrNotExist}
}

// shardReport writes an empty test.json report into a fresh directory, and
// returns the directory: a shard ReadShards reads.
func shardReport(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "test.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := ui.NewReport("test").WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Every binding in the declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestTheMergeFlowBuilds(t *testing.T) {
	f, err := NewMerge()
	if err != nil {
		t.Fatalf("building %s: %v", MergeName, err)
	}
	if f.Name() != MergeName {
		t.Errorf("Name() = %q, want %q", f.Name(), MergeName)
	}
}

// MergeParams.Inputs supplies exactly the inputs the flow reads. A key the
// flow reads and the params leave out is a run refused before its first
// stage; a key they supply and nothing reads is a value the caller believes
// reaches a stage and does not.
func TestMergeParamsSupplyExactlyTheInputsTheFlowReads(t *testing.T) {
	f, err := NewMerge()
	if err != nil {
		t.Fatal(err)
	}
	want, got := slices.Sorted(maps.Keys(f.Inputs())), slices.Sorted(maps.Keys(MergeParams{}.Inputs()))
	if !slices.Equal(want, got) {
		t.Fatalf("the flow reads %v, and its params supply %v", want, got)
	}
}

// A declaration naming no component stops after it is loaded: a fold over no
// component cannot report a shard that died, and what that means is the
// caller's to say, not a stage's.
func TestAMergeOverNoComponentReadsNothingPastTheDeclaration(t *testing.T) {
	root := t.TempDir()
	write(t, root, component.FileName, "components: []\n")
	f, err := NewMerge()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.Run(t.Context(), MergeParams{Dir: root, Reports: []string{t.TempDir()}, Reader: &measurementsReader{}}.Inputs())
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	loaded, err := flow.Output[teststages.LoadMergeComponentsOut](r, StageLoadMergeComponents)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Declared {
		t.Fatal("an empty declaration was read as naming a component")
	}
	for _, stage := range []string{StageReadShards, StageReadShardMeasurements} {
		if !r.Skipped(stage) {
			t.Errorf("%s is %v, want skipped", stage, r.Status(stage))
		}
	}
}

// ReadShardMeasurements reads the very shards ReadShards found: the Shards
// each stage's Out carries is the same slice, so a shard's measurements reach
// the fold through the flow's own wiring, not by chance.
func TestReadShardMeasurementsReadsTheShardsReadShardsFound(t *testing.T) {
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: api\n    dir: api\n    runner: go-test\n")
	write(t, root, "api/.keep", "")

	measured := shardReport(t)
	missing := t.TempDir()

	f, err := NewMerge()
	if err != nil {
		t.Fatal(err)
	}
	reader := &measurementsReader{errs: map[string]error{measured: nil}}
	r, err := f.Run(t.Context(), MergeParams{Dir: root, Reports: []string{measured, missing}, Reader: reader}.Inputs())
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	measurements, err := flow.Output[teststages.ReadShardMeasurementsOut](r, StageReadShardMeasurements)
	if err != nil {
		t.Fatal(err)
	}
	want := []teststages.ShardMeasurements{
		{Dir: measured, Read: true},
		{Dir: missing},
	}
	if !slices.Equal(measurements.Shards, want) {
		t.Errorf("shards = %+v, want %+v", measurements.Shards, want)
	}
}
