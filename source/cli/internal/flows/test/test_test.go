package testflow

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"lydite/lydite/internal/flow"
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

// Params.Inputs supplies exactly the inputs the flow reads. A key the flow
// reads and Params leaves out is a run refused before its first stage; a key
// Params supplies and nothing reads is a value the caller believes reaches a
// stage and does not.
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

// Each value Params supplies is assignable to the type the flow reads it as.
// Run checks its inputs before anything else and its context before the first
// stage, so a run under a cancelled context reaches no stage and answers the
// cancellation only when every input passed.
func TestParamsSupplyEveryInputAsTheTypeTheFlowReads(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := f.Run(ctx, (Params{}).Inputs())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the cancellation", err)
	}
	if st := r.Status(StageDeclaration); st != flow.StatusNotReached {
		t.Errorf("the first stage is %v, want %v", st, flow.StatusNotReached)
	}
}
