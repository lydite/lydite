package recordstages

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// Most recordings read no counts at all, and that is no counts rather than a
// fold of nothing: the reader is never asked.
func TestBindMutantsWithNoCountsReadHasNone(t *testing.T) {
	out, err := BindMutants(context.Background(), BindMutantsIn{Reader: &recordReader{t: t}, Tree: "deadbeef"})
	if err != nil {
		t.Fatalf("BindMutants: %v", err)
	}
	if out.Components != nil {
		t.Errorf("Components = %+v, want none", out.Components)
	}
}

// Counts taken on the tree the recording is bound to reach it, folded from
// every directory that held them in the order they were read.
func TestBindMutantsKeepsCountsOfTheBoundTree(t *testing.T) {
	counts := map[string]MutantCounts{
		"svc": {Killed: 4, TimedOut: 1, OutOfMemory: 2, Unviable: 3, Acknowledged: 5, ElapsedSeconds: 90},
	}
	var asked []string
	reader := &recordReader{t: t, foldMutants: func(dirs []string) (Mutants, error) {
		asked = dirs
		return Mutants{Tree: "deadbeef", Components: counts}, nil
	}}

	out, err := BindMutants(context.Background(), BindMutantsIn{Reader: reader, Mutated: []string{"b", "a"}, Tree: "deadbeef"})
	if err != nil {
		t.Fatalf("BindMutants: %v", err)
	}
	if !slices.Equal(asked, []string{"b", "a"}) {
		t.Errorf("folded %v, want the mutated directories in the order they were read", asked)
	}
	if !reflect.DeepEqual(out.Components, counts) {
		t.Errorf("Components = %+v, want %+v", out.Components, counts)
	}
}

// Counts naming another tree are counts this recording does not have, and
// never a refusal: the measurements that did bind are recorded whatever the
// counts beside them describe.
func TestBindMutantsDropsCountsOfAnotherTreeSilently(t *testing.T) {
	reader := &recordReader{t: t, foldMutants: func([]string) (Mutants, error) {
		return Mutants{Tree: "0000000", Components: map[string]MutantCounts{"svc": {Killed: 7}}}, nil
	}}

	out, err := BindMutants(context.Background(), BindMutantsIn{Reader: reader, Mutated: []string{"a"}, Tree: "deadbeef"})
	if err != nil {
		t.Fatalf("counts naming another tree failed the stage: %v", err)
	}
	if out.Components != nil {
		t.Errorf("Components = %+v, taken on a tree this recording is not filed against", out.Components)
	}
}

// Documents that will not fold — shards of different trees — are dropped the
// same way, and their error goes no further.
func TestBindMutantsDropsCountsThatWillNotFoldSilently(t *testing.T) {
	reader := &recordReader{t: t, foldMutants: func([]string) (Mutants, error) {
		return Mutants{Tree: "deadbeef"}, errors.New("the mutant counts describe different trees (aaaaaaa and bbbbbbb), so they are not shards of one run")
	}}

	out, err := BindMutants(context.Background(), BindMutantsIn{Reader: reader, Mutated: []string{"a", "b"}, Tree: "deadbeef"})
	if err != nil {
		t.Fatalf("counts that would not fold failed the stage: %v", err)
	}
	if out.Components != nil {
		t.Errorf("Components = %+v, want none from a fold that refused its documents", out.Components)
	}
}
