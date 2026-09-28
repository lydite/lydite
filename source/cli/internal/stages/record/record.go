// Package recordstages holds the stages that record what one or more runs
// measured: loading the tree's own declaration, reading the report
// directories, folding the measurements, binding the recording to the tree
// that is checked out, counting the scan's findings, binding the mutant
// counts, deciding the baseline, composing the quality history, and the one
// write that lands both. Named apart from recordflow (internal/flows/record),
// which wires these stages and shares their directory name, so the flow
// definition imports both without renaming either — and apart from the
// `lydite test record` command in cmd/lydite, which renders every row these
// stages' outcomes become.
//
// Every stage is a plain function of its own In. Nothing here reads a report
// document itself: each is written by another command, in that command's own
// types, so a ReportReader reads and folds them and hands over only the
// boundary values below. Nothing here renders either — a stage returns what it
// found, and the rows and their wording are the command's.
package recordstages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
)

// Measurements is what a `lydite test` run measured, as far as a recording
// reads it: one directory's document, or every directory's folded into one.
type Measurements struct {
	// Tree is the tree these measurements describe, which is what binds a
	// recording to the checkout it may be recorded from.
	Tree string
	// Components is each component's entry, keyed by its declared name.
	Components map[string]Measurement
	// Tests is what became of each component's suite, for the components the
	// run ran. A component whose suite failed has counts here and no entry in
	// Components.
	Tests map[string]junit.Counts
	// Reason says why there is nothing to record, and is empty exactly when
	// Components holds something.
	Reason string
	// Snapshot is the measurements as the lydite branch stores them, with
	// everything that says how one run arrived at an entry dropped. The
	// reader takes it, because the document's own type decides what that is.
	Snapshot gitstate.Snapshot
}

// Measurement is one component's entry in Measurements.
type Measurement struct {
	gitstate.Entry
	// Unanchored is the counts the run took, before a within-tolerance dip
	// was anchored back to the baseline's percentage. Nil when the two agree.
	// The history records what was measured, and the baseline what was
	// anchored, so both are carried.
	Unanchored *coverage.LineCount
	// CRAP is the component's complexity scalars, nil for a component lydite
	// scores none of and for one whose score could not be taken.
	CRAP *gitstate.CRAPEntry
}

// Mutants is what a `lydite mutation` run made of its mutants: one
// directory's document, or every directory's folded into one.
type Mutants struct {
	// Tree is the tree the mutants came from, which binds the counts to the
	// checkout exactly as Measurements.Tree binds the measurements.
	Tree string
	// Components is the counts for each component that ran. A component that
	// did not run is absent, never present with zeros.
	Components map[string]MutantCounts
}

// MutantCounts is what became of one component's mutants, and how long that
// took.
type MutantCounts struct {
	Killed       int
	TimedOut     int
	OutOfMemory  int
	Survived     int
	Unviable     int
	Acknowledged int
	// ElapsedSeconds is how long the component took, nought when the
	// document recorded none.
	ElapsedSeconds float64
}

// Scan is what a `lydite scan` run's document says about its findings.
type Scan struct {
	// Findings is every located claim the scan made.
	Findings []finding.Finding
	// Crashed is every gate that did not finish a trustworthy scan, whose
	// findings are therefore not a complete answer.
	Crashed []finding.Crash
}

// ReportReader reads and folds the documents a recording's report directories
// hold.
//
// Each document is written by another command, in that command's own types
// and by that command's own rules, so reading and folding stay with the code
// that owns them and a stage sees only the values above. Every error is
// returned exactly as the owning code produced it: a document that is simply
// not there is an error satisfying errors.Is(err, os.ErrNotExist), a document
// that is there and will not parse is one that does not, and the text of
// either is what a reader of the recording is shown.
type ReportReader interface {
	// ReadMeasurements reads the measurements document in dir.
	ReadMeasurements(dir string) (Measurements, error)
	// ReadScan reads the scan document in dir.
	ReadScan(dir string) (Scan, error)
	// ReadMutants reads the mutant-counts document in dir.
	ReadMutants(dir string) (Mutants, error)
	// FoldMeasurements folds the measurements documents ReadMeasurements
	// answered for dirs, in that order, into one. It is only ever given
	// directories ReadMeasurements answered without an error, and folds what
	// those reads returned rather than reading again.
	FoldMeasurements(dirs []string) (Measurements, error)
	// FoldMutants folds the mutant-counts documents ReadMutants answered for
	// dirs, in that order, into one, on the same terms as FoldMeasurements.
	FoldMutants(dirs []string) (Mutants, error)
}

// LoadDeclarationIn names the tree whose declaration applies.
type LoadDeclarationIn struct {
	// Dir is the root directory of the tree being recorded.
	Dir string
}

// LoadDeclarationOut is the tree's own statements about itself.
type LoadDeclarationOut struct {
	// Declaration says which components a complete baseline must cover and
	// which gates each one's language implies.
	Declaration component.File
	// Config is the tolerance this tree accepts, and which of its languages
	// are checked at all.
	Config config.Config
}

// LoadDeclaration reads the component declaration and the configuration from
// the tree being recorded.
//
// From the tree and never from the documents: the declaration says what a
// complete recording must hold, and taking it from the documents whose
// completeness is in question would make that check answer itself.
func LoadDeclaration(_ context.Context, in LoadDeclarationIn) (LoadDeclarationOut, error) {
	decl, err := component.Load(in.Dir)
	if err != nil {
		return LoadDeclarationOut{}, fmt.Errorf("reading %s: %w", component.FileName, err)
	}
	cfg, err := config.Load(in.Dir)
	if err != nil {
		return LoadDeclarationOut{}, fmt.Errorf("reading %s: %w", config.FileName, err)
	}
	return LoadDeclarationOut{Declaration: decl, Config: cfg}, nil
}
