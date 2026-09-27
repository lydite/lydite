package mutationflow

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"lydite/lydite/internal/flow"
	mutationstages "lydite/lydite/internal/stages/mutation"
)

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

// A repository declaring no component stops after the declaration: no shard,
// count or log is read, since a fold over no component has nothing to say
// about any of them.
func TestAFoldOverNoComponentReadsNothingPastTheDeclaration(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lydite"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lydite", "components.yml"), []byte("components: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := NewMerge()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.Run(t.Context(), MergeParams{Dir: root, Reports: []string{t.TempDir()}, LogName: "mutation.log"}.Inputs())
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	loaded, err := flow.Output[mutationstages.LoadComponentsOut](r, StageLoadComponents)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Declared {
		t.Fatal("an empty declaration was read as naming a component")
	}
	for _, stage := range []string{StageReadShards, StageReadShardCounts, StageFoldShardCounts, StageReadProjections} {
		if !r.Skipped(stage) {
			t.Errorf("%s is %v, want skipped", stage, r.Status(stage))
		}
	}
}
