package mutationstages

import (
	"context"
	"fmt"
	"io"
	"os"

	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/mutation"
)

// RecordMutantsIn is what a run made of its mutants, and where it is written.
type RecordMutantsIn struct {
	// Dir is the scan root, whose HEAD tree the counts are recorded against.
	Dir string
	// ReportsDir is the run's reports directory, and Ignore keeps it out of
	// git. Ignore is best-effort: a directory that cannot hold what keeps it
	// ignored is no reason to lose the counts.
	ReportsDir string
	Ignore     func(dir string) error
	// Components is the outcomes whose verdict stands. After an interrupted
	// run the caller passes only the outcomes that survived its own withdrawal
	// of interrupted failures, since only the caller knows which verdicts it
	// withdrew; every outcome here whose Ran is true is recorded.
	Components []ComponentOutcome
	// Warnings is where a failure to record is said. Nil discards.
	Warnings io.Writer
}

// RecordMutantsOut is empty: what RecordMutants produces is the file.
type RecordMutantsOut struct{}

// RecordMutants writes what the run made of its mutants beside its report.
//
// Only the components whose mutants ran to a summary are in the document. A
// component that did not run is absent rather than
// present with zeros, which is the distinction the document exists to carry: a
// zeroed entry for a component nothing mutated reads, permanently, as a suite
// that killed everything. A run that mutated no component still names its
// tree, which is what a later fold needs to tell a shard that ran nothing from
// a shard whose job died.
//
// The tree is resolved out from under any cancellation, because a run cut
// short still records the components that finished. It never fails: every
// failure is a warning, since the mutants ran, their verdict is in the report,
// and losing the byproduct is not a reason to discard it.
func RecordMutants(ctx context.Context, in RecordMutantsIn) (RecordMutantsOut, error) {
	if in.Warnings == nil {
		in.Warnings = io.Discard
	}
	tree, err := gitstate.TreeSHA(context.WithoutCancel(ctx), in.Dir, "HEAD")
	if err != nil {
		_, _ = fmt.Fprintf(in.Warnings, "warning: could not resolve this tree, so the mutant counts were not written: %v\n", err)
		return RecordMutantsOut{}, nil
	}
	if err := writeCounts(in.ReportsDir, in.Ignore, countsOf(tree, in.Components)); err != nil {
		_, _ = fmt.Fprintf(in.Warnings, "warning: could not write the mutant counts: %v\n", err)
	}
	return RecordMutantsOut{}, nil
}

// countsOf is the document one run hands on: the tree it mutated, and the
// counts for exactly the components that ran.
func countsOf(tree string, outcomes []ComponentOutcome) mutation.CountsDocument {
	doc := mutation.CountsDocument{Tree: tree}
	for _, o := range outcomes {
		if !o.Ran() {
			continue
		}
		if doc.Components == nil {
			doc.Components = map[string]mutation.ComponentCounts{}
		}
		doc.Components[o.Component.Name] = mutation.CountsOf(o.Summary, o.Elapsed)
	}
	return doc
}

// writeCounts creates the reports directory, keeps it out of git, and writes
// the document into it, in that order.
func writeCounts(dir string, ignore func(string) error, doc mutation.CountsDocument) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	_ = ignore(dir)
	return mutation.WriteCounts(dir, doc)
}
