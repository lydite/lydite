package testflow

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/scheduler"
	teststages "lydite/lydite/internal/stages/test"
)

// write saves body at root/rel, creating every directory above it.
func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every binding in the declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestThePlanFlowBuilds(t *testing.T) {
	f, err := NewPlan()
	if err != nil {
		t.Fatalf("building %s: %v", PlanName, err)
	}
	if f.Name() != PlanName {
		t.Errorf("Name() = %q, want %q", f.Name(), PlanName)
	}
}

// PlanParams.Inputs supplies exactly the inputs the flow reads. A key the
// flow reads and the params leave out is a run refused before its first
// stage; a key they supply and nothing reads is a value the caller believes
// reaches a stage and does not.
func TestPlanParamsSupplyExactlyTheInputsTheFlowReads(t *testing.T) {
	f, err := NewPlan()
	if err != nil {
		t.Fatal(err)
	}
	want, got := slices.Sorted(maps.Keys(f.Inputs())), slices.Sorted(maps.Keys(PlanParams{}.Inputs()))
	if !slices.Equal(want, got) {
		t.Fatalf("the flow reads %v, and its params supply %v", want, got)
	}
}

// A declaration naming no component stops after it is loaded: grouping has
// nothing to group, and what that means for the matrix is the caller's to
// say, not GroupShards'.
func TestAPlanOverNoComponentReadsNothingPastTheDeclaration(t *testing.T) {
	root := t.TempDir()
	write(t, root, component.FileName, "components: []\n")
	f, err := NewPlan()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.Run(t.Context(), PlanParams{Dir: root}.Inputs())
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	loaded, err := flow.Output[teststages.LoadPlanComponentsOut](r, StageLoadPlanComponents)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Declared {
		t.Fatal("an empty declaration was read as naming a component")
	}
	if !r.Skipped(StageGroupShards) {
		t.Errorf("%s is %v, want skipped", StageGroupShards, r.Status(StageGroupShards))
	}
}

// GroupShards groups the very declaration LoadPlanComponents read: the File
// each stage's Out carries is the same one, so a compose file's host ports
// reach the shard grouping through the flow's own wiring, not by chance.
func TestGroupShardsGroupsTheDeclarationLoadPlanComponentsRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: api\n    dir: go/api\n    runner: go-test\n    compose:\n      file: compose.yml\n"+
			"  - name: web\n    dir: web\n    runner: vitest\n    compose:\n      file: compose.yml\n")
	service := func(dir, name string) {
		write(t, root, dir+"/compose.yml",
			"services:\n  "+name+":\n    image: postgres\n    ports: [\"5432:5432\"]\n"+
				"    healthcheck:\n      test: [\"CMD\", \"true\"]\n")
	}
	service("go/api", "db")
	service("web", "cache")
	f, err := NewPlan()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.Run(t.Context(), PlanParams{Dir: root}.Inputs())
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	grouped, err := flow.Output[teststages.GroupShardsOut](r, StageGroupShards)
	if err != nil {
		t.Fatal(err)
	}
	want := []teststages.PlanShard{
		{Name: "api-web", Components: []string{"api", "web"},
			Conflicts: []scheduler.Conflict{{A: "api", B: "web", On: "port 5432"}}},
	}
	if !slices.EqualFunc(grouped.Shards, want, func(a, b teststages.PlanShard) bool {
		return a.Name == b.Name && slices.Equal(a.Components, b.Components) && slices.Equal(a.Conflicts, b.Conflicts)
	}) {
		t.Errorf("shards = %+v\nwant     %+v", grouped.Shards, want)
	}
}
