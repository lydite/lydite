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

// Every binding in each declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestTheFlowsBuild(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func() (*flow.Flow, error)
	}{
		{Name, New},
		{RecordName, NewRecord},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := c.build()
			if err != nil {
				t.Fatalf("building %s: %v", c.name, err)
			}
			if f.Name() != c.name {
				t.Errorf("Name() = %q, want %q", f.Name(), c.name)
			}
		})
	}
}

// Params.Inputs and RecordParams.Inputs supply exactly the inputs their flow
// reads. A key the flow reads and the params leave out is a run refused before
// its first stage; a key they supply and nothing reads is a value the caller
// believes reaches a stage and does not.
func TestParamsSupplyExactlyTheInputsTheFlowReads(t *testing.T) {
	for _, c := range []struct {
		name   string
		build  func() (*flow.Flow, error)
		inputs flow.Inputs
	}{
		{Name, New, Params{}.Inputs()},
		{RecordName, NewRecord, RecordParams{}.Inputs()},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := c.build()
			if err != nil {
				t.Fatal(err)
			}
			want, got := slices.Sorted(maps.Keys(f.Inputs())), slices.Sorted(maps.Keys(c.inputs))
			if !slices.Equal(want, got) {
				t.Fatalf("the flow reads %v, and its params supply %v", want, got)
			}
		})
	}
}

// A repository declaring no component stops after the declaration: nothing
// is provisioned, resolved or run, and none of the injected helpers — every one
// of them nil here — is reached.
func TestADeclarationNamingNoComponentRunsNothingPastIt(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lydite"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lydite", "components.yml"), []byte("components: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := New()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.Run(t.Context(), Params{Dir: root}.Inputs())
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	loaded, err := flow.Output[mutationstages.LoadDeclarationOut](r, StageLoadDeclaration)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Declared {
		t.Fatal("an empty declaration was read as naming a component")
	}
	for _, stage := range []string{StageProvisionToolchains, StageResolveBase, StageSelectAffected, StageScopeChange, StageRunMutants} {
		if !r.Skipped(stage) {
			t.Errorf("%s is %v, want skipped", stage, r.Status(stage))
		}
	}
}
