package recordstages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/gitstate"
)

// BindTreeIn is the checkout, and the measurements that must describe it.
type BindTreeIn struct {
	// Dir is the checkout being recorded.
	Dir string
	// Folded is FoldMeasurementsOut.Folded.
	Folded Measurements
}

// BindTreeOut is whether the measurements describe the tree that is checked
// out.
type BindTreeOut struct {
	// Bound reports that they do. Nothing past this stage records anything
	// when it does not hold.
	Bound bool
	// Head is the tree that is checked out.
	Head string
	// Measured is the tree the measurements describe.
	Measured string
}

// BindTree binds a recording to the tree that is checked out.
//
// A document names the tree it measured, and a recording is filed nowhere
// else: without the binding a mis-wired workflow lands one tree's numbers
// under another tree's key, silently, and that entry then gates every later
// change whose merge-base is that tree. It binds the history as well as the
// baseline, because a document describing another tree describes another
// commit, and a record filed against this one would be a data point nobody
// measured.
//
// A mismatch is an answer rather than an error: the documents were read, and
// what they say is that they must not be recorded here. An error is reserved
// for a checkout whose tree cannot be resolved at all.
func BindTree(ctx context.Context, in BindTreeIn) (BindTreeOut, error) {
	head, err := gitstate.TreeSHA(ctx, in.Dir, "HEAD")
	if err != nil {
		return BindTreeOut{}, fmt.Errorf("resolving the tree that is checked out: %w", err)
	}
	return BindTreeOut{Bound: head == in.Folded.Tree, Head: head, Measured: in.Folded.Tree}, nil
}
