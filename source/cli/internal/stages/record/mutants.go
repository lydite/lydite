package recordstages

import "context"

// BindMutantsIn is the directories whose mutant counts were read, and the tree
// the recording is bound to.
type BindMutantsIn struct {
	Reader ReportReader
	// Mutated is ReadReportsOut.Mutated.
	Mutated []string
	// Tree is BindTreeOut.Head, the tree the measurements were bound to.
	Tree string
}

// BindMutantsOut is the mutant counts this recording has.
type BindMutantsOut struct {
	// Components is each mutated component's counts, nil when no document was
	// read, when the documents would not fold, and when they describe another
	// tree.
	Components map[string]MutantCounts
}

// BindMutants is the folded mutant counts for the tree being recorded, and
// nothing at all when they describe another one.
//
// The binding is the same one the measurements carry and the answer to failing
// it is not: measurements name the tree a record is filed against, so a
// document describing another tree fails the recording, while counts that
// cannot be tied to this checkout are simply counts this recording does not
// have. Absence is a legitimate answer for this document at every step — most
// recordings read none — so degrading to it loses nothing a reader could have
// acted on, and refusing the whole recording over it would drop the coverage
// and test series of a commit that measured both. A fold that refuses its
// documents is dropped the same way, and for the same reason.
func BindMutants(_ context.Context, in BindMutantsIn) (BindMutantsOut, error) {
	if len(in.Mutated) == 0 {
		return BindMutantsOut{}, nil
	}
	folded, err := in.Reader.FoldMutants(in.Mutated)
	if err != nil || folded.Tree != in.Tree {
		return BindMutantsOut{}, nil
	}
	return BindMutantsOut{Components: folded.Components}, nil
}
