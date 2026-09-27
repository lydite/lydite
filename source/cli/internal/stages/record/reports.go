package recordstages

import (
	"context"
	"errors"

	"lydite/lydite/internal/finding"
)

// ErrNoMeasurements is FoldMeasurements' answer when none of the report
// directories held a readable measurements document.
var ErrNoMeasurements = errors.New("none of the named report directories holds a measurements document")

// Directory is what one report directory held: each document read out of it,
// or the error reading that document gave.
//
// Three documents and not one, because each is written by a different command
// in a different job — `lydite test` what it measured, `lydite scan` what it
// found, `lydite mutation` what became of its mutants — and a directory
// holding only one of them is the ordinary shape of each of those jobs. Every
// error is kept as the reader returned it, an absent document's included:
// whether an absence is worth saying depends on what else the directory held,
// which is the command's to judge when it says what came out of here.
type Directory struct {
	// Dir is the report directory, as it was named.
	Dir string
	// Measurements is the directory's measurements document, zero when
	// MeasurementsErr is set.
	Measurements    Measurements
	MeasurementsErr error
	// Scan is the directory's scan document, zero when ScanErr is set.
	Scan    Scan
	ScanErr error
	// Mutants is the directory's mutant-counts document, zero when
	// MutantsErr is set.
	Mutants    Mutants
	MutantsErr error
}

// ReadReportsIn is the report directories a recording is folded from.
type ReadReportsIn struct {
	Reader ReportReader
	// Reports is each report directory, in the order it was named.
	Reports []string
}

// ReadReportsOut is what every directory held, and what the recording takes
// from them.
type ReadReportsOut struct {
	// Directories is each directory's documents, in the order they were
	// named.
	Directories []Directory
	// Measured is each directory whose measurements document was read, in
	// order, which is what the fold and the tree binding are made from.
	Measured []string
	// Mutated is each directory whose mutant-counts document was read, in
	// order.
	Mutated []string
	// Found is every located claim the scan documents made.
	Found []finding.Finding
	// Crashed is every gate the scan documents name as not having finished a
	// trustworthy scan.
	Crashed []finding.Crash
	// Scanned says that some directory held a readable scan document, which
	// is what separates a gate that found nothing from a scan that never ran:
	// both are the same empty list of findings, and only this tells them
	// apart.
	Scanned bool
}

// ReadReports reads each document out of each named directory, in the order
// they were named: the measurements, then the scan, then the mutant counts.
//
// No directory fails the stage, whatever it held. A run with --no-coverage
// writes no measurements, and a caller passing the same directory list to
// `record` as to `publish` is doing something reasonable; whether the set as a
// whole holds enough to record is FoldMeasurements' question.
func ReadReports(_ context.Context, in ReadReportsIn) (ReadReportsOut, error) {
	var out ReadReportsOut
	for _, dir := range in.Reports {
		d := Directory{Dir: dir}

		d.Measurements, d.MeasurementsErr = in.Reader.ReadMeasurements(dir)
		if d.MeasurementsErr == nil {
			out.Measured = append(out.Measured, dir)
		}

		d.Scan, d.ScanErr = in.Reader.ReadScan(dir)
		if d.ScanErr == nil {
			out.Found = append(out.Found, d.Scan.Findings...)
			out.Crashed = append(out.Crashed, d.Scan.Crashed...)
			out.Scanned = true
		}

		d.Mutants, d.MutantsErr = in.Reader.ReadMutants(dir)
		if d.MutantsErr == nil {
			out.Mutated = append(out.Mutated, dir)
		}

		out.Directories = append(out.Directories, d)
	}
	return out, nil
}

// FoldMeasurementsIn is the directories whose measurements are folded.
type FoldMeasurementsIn struct {
	Reader ReportReader
	// Measured is ReadReportsOut.Measured.
	Measured []string
}

// FoldMeasurementsOut is the one set of measurements a recording lands.
type FoldMeasurementsOut struct {
	Folded Measurements
}

// FoldMeasurements folds every measurements document read into one.
//
// A measurements document is required and a scan document is not, because
// only the first names the tree it describes. That name is what binds a
// recording to the checkout, so a set of directories holding scans alone could
// be recorded against nothing, and is refused with ErrNoMeasurements rather
// than filed against a commit this stage would have had to guess at. An error
// the fold itself gives — documents describing different trees — is returned
// as the reader gave it.
func FoldMeasurements(_ context.Context, in FoldMeasurementsIn) (FoldMeasurementsOut, error) {
	if len(in.Measured) == 0 {
		return FoldMeasurementsOut{}, ErrNoMeasurements
	}
	folded, err := in.Reader.FoldMeasurements(in.Measured)
	if err != nil {
		return FoldMeasurementsOut{}, err
	}
	return FoldMeasurementsOut{Folded: folded}, nil
}
