package recordstages

import (
	"context"

	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/ledger"
)

// WriteStateIn is everything a recording lands on the state branch.
type WriteStateIn struct {
	// Dir is the checkout being recorded.
	Dir string
	// Head is BindTreeOut.Head, the tree the baseline is keyed by.
	Head string
	// Snapshot is DecideBaselineOut.Snapshot, empty when there is no
	// baseline to land.
	Snapshot gitstate.Snapshot
	// Records is ComposeHistoryOut.Records, nil when there is no history to
	// append.
	Records gitstate.Records
}

// WriteStateOut is what reached the branch.
type WriteStateOut struct {
	// Landed is the records that actually landed, which is not the same as
	// the ones Records offered: a record already on the branch is not
	// appended again, so a commit recorded twice lands nothing the second
	// time.
	Landed []ledger.Record
}

// WriteState lands the baseline and the quality history in one commit on the
// state branch.
//
// The one place a recording reaches the state branch, over both policies and
// whichever of them has something to say. A second call site is a second
// place state can reach the branch, which is the invariant a recording rests
// on and which a grep for `gitstate.Write` answers. One commit and never two,
// because a baseline landed with no record beside it is a hole in the history
// the write path itself invented.
//
// A write that never lands is returned as the error gitstate.Write gave, and
// never folded into the outcome: what a failed write means — a recording that
// failed, or a routine push race over history alone — depends on what was
// being landed, which is not this stage's to judge.
func WriteState(ctx context.Context, in WriteStateIn) (WriteStateOut, error) {
	landed, err := gitstate.Write(ctx, in.Dir, in.Head, in.Snapshot, in.Records)
	if err != nil {
		return WriteStateOut{}, err
	}
	return WriteStateOut{Landed: landed}, nil
}
