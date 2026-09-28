package reviewflow

import (
	"maps"
	"slices"
	"testing"
)

// Every binding in the declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestTheReviewFlowBuilds(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.Name() != Name {
		t.Errorf("Name() = %q, want %q", f.Name(), Name)
	}
}

// Params.Inputs supplies exactly the inputs the flow reads, each assignable
// to the type the flow reads it as. A key the flow reads and Params leaves
// out is a run refused before its first stage; a key Params supplies and
// nothing reads is a value the caller believes reaches a stage and does not.
func TestReviewParamsSupplyExactlyTheInputsTheFlowReads(t *testing.T) {
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

// The review-compare flow builds the same way, and reads no input the
// review flow's own publishing route needs: it is nothing but the comparison
// a --publish run may read back later.
func TestTheReviewCompareFlowBuilds(t *testing.T) {
	f, err := NewCompare()
	if err != nil {
		t.Fatalf("NewCompare: %v", err)
	}
	if f.Name() != CompareName {
		t.Errorf("Name() = %q, want %q", f.Name(), CompareName)
	}
}

// CompareParams.Inputs supplies exactly the inputs the review-compare flow
// reads.
func TestCompareParamsSupplyExactlyTheInputsTheFlowReads(t *testing.T) {
	f, err := NewCompare()
	if err != nil {
		t.Fatalf("NewCompare: %v", err)
	}
	want := f.Inputs()
	got := (CompareParams{}).Inputs()
	if w, g := slices.Sorted(maps.Keys(want)), slices.Sorted(maps.Keys(got)); !slices.Equal(w, g) {
		t.Fatalf("the flow reads %v, and CompareParams supplies %v", w, g)
	}
}
