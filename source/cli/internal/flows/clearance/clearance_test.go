package clearanceflow

import (
	"maps"
	"slices"
	"testing"
)

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
}

// Rendering is chosen by naming a path, and only by that: the flow's two
// recording stages are conditioned on one bool, and it is never left to a
// caller to set it beside the path it has to agree with.
func TestRenderingIsChosenByNamingAPath(t *testing.T) {
	if (Params{}).Inputs()[InputRender] != false {
		t.Error("a run naming no path renders")
	}
	if (Params{StatusOut: "status.json"}).Inputs()[InputRender] != true {
		t.Error("a run naming a path posts")
	}
}
