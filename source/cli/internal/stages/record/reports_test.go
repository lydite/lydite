package recordstages

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"testing"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
)

// recordRead is one document's answer from recordReader: its value, or the
// error reading it gives.
type recordRead[T any] struct {
	value T
	err   error
}

// recordReader is a ReportReader answering from fixed per-directory tables. A
// directory a table does not name answers the not-exist error a real read of
// it would, and a fold nothing in the test case expects fails the test.
type recordReader struct {
	t *testing.T

	measurements map[string]recordRead[Measurements]
	scans        map[string]recordRead[Scan]
	mutants      map[string]recordRead[Mutants]

	// calls is each read in the order it was made, as "<document>(<dir>)".
	calls []string

	foldMeasurements func(dirs []string) (Measurements, error)
	foldMutants      func(dirs []string) (Mutants, error)
}

func recordAbsent(dir, name string) error {
	return &fs.PathError{Op: "open", Path: dir + "/" + name, Err: fs.ErrNotExist}
}

func (r *recordReader) ReadMeasurements(dir string) (Measurements, error) {
	r.calls = append(r.calls, "measurements("+dir+")")
	got, ok := r.measurements[dir]
	if !ok {
		return Measurements{}, recordAbsent(dir, "measurements.json")
	}
	return got.value, got.err
}

func (r *recordReader) ReadScan(dir string) (Scan, error) {
	r.calls = append(r.calls, "scan("+dir+")")
	got, ok := r.scans[dir]
	if !ok {
		return Scan{}, recordAbsent(dir, "scan.json")
	}
	return got.value, got.err
}

func (r *recordReader) ReadMutants(dir string) (Mutants, error) {
	r.calls = append(r.calls, "mutants("+dir+")")
	got, ok := r.mutants[dir]
	if !ok {
		return Mutants{}, recordAbsent(dir, "mutants.json")
	}
	return got.value, got.err
}

func (r *recordReader) FoldMeasurements(dirs []string) (Measurements, error) {
	if r.foldMeasurements == nil {
		r.t.Fatal("FoldMeasurements: unexpected call")
	}
	return r.foldMeasurements(dirs)
}

func (r *recordReader) FoldMutants(dirs []string) (Mutants, error) {
	if r.foldMutants == nil {
		r.t.Fatal("FoldMutants: unexpected call")
	}
	return r.foldMutants(dirs)
}

// Every directory is asked for every document, in the order the directories
// were named and in one fixed order within each, and what each answered is
// kept beside the directory it came from.
func TestReadReportsKeepsWhatEachDocumentAnsweredPerDirectory(t *testing.T) {
	gosec := finding.Finding{Gate: "gosec", Component: "svc", Path: "svc/a.go", Rule: "G401"}
	leak := finding.Finding{Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key"}
	crash := finding.Crash{Gate: "govulncheck", Component: "svc"}
	shard := Measurements{Tree: "deadbeef", Components: map[string]Measurement{"svc": {}}}
	counts := Mutants{Tree: "deadbeef", Components: map[string]MutantCounts{"svc": {Killed: 3}}}
	unparseable := errors.New("scan/scan.json: invalid character 'n'")
	reader := &recordReader{
		t:            t,
		measurements: map[string]recordRead[Measurements]{"shard": {value: shard}},
		scans: map[string]recordRead[Scan]{
			"scan":  {value: Scan{Findings: []finding.Finding{gosec, leak}, Crashed: []finding.Crash{crash}}},
			"shard": {err: unparseable},
		},
		mutants: map[string]recordRead[Mutants]{"mutation": {value: counts}},
	}

	out, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"shard", "scan", "mutation"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}

	wantCalls := []string{
		"measurements(shard)", "scan(shard)", "mutants(shard)",
		"measurements(scan)", "scan(scan)", "mutants(scan)",
		"measurements(mutation)", "scan(mutation)", "mutants(mutation)",
	}
	if !slices.Equal(reader.calls, wantCalls) {
		t.Errorf("reads = %v, want %v", reader.calls, wantCalls)
	}
	if len(out.Directories) != 3 {
		t.Fatalf("%d directories, want one per directory named", len(out.Directories))
	}
	for i, dir := range []string{"shard", "scan", "mutation"} {
		if out.Directories[i].Dir != dir {
			t.Errorf("Directories[%d].Dir = %q, want %q", i, out.Directories[i].Dir, dir)
		}
	}

	first := out.Directories[0]
	if first.MeasurementsErr != nil || !reflect.DeepEqual(first.Measurements, shard) {
		t.Errorf("shard's measurements = %+v, %v; want the document it held", first.Measurements, first.MeasurementsErr)
	}
	if first.ScanErr != unparseable {
		t.Errorf("shard's scan error = %v, want the reader's own", first.ScanErr)
	}
	if !errors.Is(first.MutantsErr, os.ErrNotExist) {
		t.Errorf("shard's mutants error = %v, want the absence the reader reported", first.MutantsErr)
	}

	second := out.Directories[1]
	if !errors.Is(second.MeasurementsErr, os.ErrNotExist) || second.ScanErr != nil {
		t.Errorf("scan's documents = %v, %v; want no measurements and its scan", second.MeasurementsErr, second.ScanErr)
	}
	if third := out.Directories[2]; third.MutantsErr != nil || !reflect.DeepEqual(third.Mutants, counts) {
		t.Errorf("mutation's counts = %+v, %v; want the document it held", third.Mutants, third.MutantsErr)
	}

	if !slices.Equal(out.Measured, []string{"shard"}) {
		t.Errorf("Measured = %v, want only the directory whose measurements read", out.Measured)
	}
	if !slices.Equal(out.Mutated, []string{"mutation"}) {
		t.Errorf("Mutated = %v, want only the directory whose counts read", out.Mutated)
	}
	if !reflect.DeepEqual(out.Found, []finding.Finding{gosec, leak}) {
		t.Errorf("Found = %+v, want the readable scan's claims", out.Found)
	}
	if !reflect.DeepEqual(out.Crashed, []finding.Crash{crash}) {
		t.Errorf("Crashed = %+v, want the readable scan's crashes", out.Crashed)
	}
	if !out.Scanned {
		t.Error("a directory held a readable scan, and Scanned is false")
	}
}

// An absent document's error reaches the outcome as the reader gave it, text
// included, because that text is what a directory that yielded nothing says
// for itself.
func TestReadReportsPassesAnAbsentDocumentsErrorThroughUnchanged(t *testing.T) {
	reader := &recordReader{t: t}

	out, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"empty"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}

	d := out.Directories[0]
	want := recordAbsent("empty", "measurements.json").Error()
	if d.MeasurementsErr == nil || d.MeasurementsErr.Error() != want {
		t.Errorf("MeasurementsErr = %v, want %q", d.MeasurementsErr, want)
	}
	if out.Scanned || len(out.Measured) != 0 || len(out.Mutated) != 0 {
		t.Errorf("an empty directory yielded %+v", out)
	}
}

// A scan document that will not parse is not a scan that ran. Only a
// readable one separates a clean gate from one that never looked.
func TestReadReportsCountsNoUnreadableScanAsScanned(t *testing.T) {
	reader := &recordReader{t: t, scans: map[string]recordRead[Scan]{
		"dir": {value: Scan{Findings: []finding.Finding{{Gate: "gosec"}}}, err: errors.New("dir/scan.json: unexpected EOF")},
	}}

	out, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"dir"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}
	if out.Scanned || len(out.Found) != 0 {
		t.Errorf("Scanned = %t, Found = %+v; want nothing from a document that would not read", out.Scanned, out.Found)
	}
}

// The same directory named twice is read twice, and folded twice, exactly as
// it was named.
func TestReadReportsReadsARepeatedDirectoryEachTimeItIsNamed(t *testing.T) {
	reader := &recordReader{t: t, measurements: map[string]recordRead[Measurements]{
		"dir": {value: Measurements{Tree: "deadbeef"}},
	}}

	out, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"dir", "dir"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}
	if len(out.Directories) != 2 || !slices.Equal(out.Measured, []string{"dir", "dir"}) {
		t.Errorf("Directories = %d, Measured = %v; want the directory once per naming", len(out.Directories), out.Measured)
	}
}

// Scans alone name no tree, so there is nothing to bind a recording to: the
// fold is refused before the reader is asked to make it.
func TestFoldMeasurementsRefusesWhenNothingWasMeasured(t *testing.T) {
	_, err := FoldMeasurements(context.Background(), FoldMeasurementsIn{Reader: &recordReader{t: t}})
	if !errors.Is(err, ErrNoMeasurements) {
		t.Errorf("err = %v, want ErrNoMeasurements", err)
	}
}

func TestFoldMeasurementsFoldsTheMeasuredDirectoriesInOrder(t *testing.T) {
	folded := Measurements{
		Tree:       "deadbeef",
		Components: map[string]Measurement{"svc": {Entry: gitstate.Entry{Producer: "go"}}},
		Snapshot:   gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": {Producer: "go"}}},
	}
	var asked []string
	reader := &recordReader{t: t, foldMeasurements: func(dirs []string) (Measurements, error) {
		asked = dirs
		return folded, nil
	}}

	out, err := FoldMeasurements(context.Background(), FoldMeasurementsIn{Reader: reader, Measured: []string{"b", "a"}})
	if err != nil {
		t.Fatalf("FoldMeasurements: %v", err)
	}
	if !slices.Equal(asked, []string{"b", "a"}) {
		t.Errorf("folded %v, want the measured directories in the order they were read", asked)
	}
	if !reflect.DeepEqual(out.Folded, folded) {
		t.Errorf("Folded = %+v, want the reader's fold", out.Folded)
	}
}

// Documents describing different trees are not shards of one run, and the
// fold's own error is the command's error, word for word.
func TestFoldMeasurementsPassesTheFoldsErrorThroughUnchanged(t *testing.T) {
	refused := errors.New("the measurements describe different trees (aaaaaaa and bbbbbbb), so they are not shards of one run")
	reader := &recordReader{t: t, foldMeasurements: func([]string) (Measurements, error) {
		return Measurements{}, refused
	}}

	_, err := FoldMeasurements(context.Background(), FoldMeasurementsIn{Reader: reader, Measured: []string{"a", "b"}})
	if err != refused {
		t.Errorf("err = %v, want the fold's own error", err)
	}
}

// A directory holding measurements and no counts is what every `lydite test`
// shard uploads: it yields its measurements, and the counts' absence is kept
// as an absence rather than a document that failed to read.
func TestMeasurementsWithNoCountsBesideThemRecordAsTheyAlwaysDid(t *testing.T) {
	reader := &recordReader{t: t, measurements: map[string]recordRead[Measurements]{
		"dir": {value: Measurements{Tree: "deadbeef"}},
	}}

	read, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"dir"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}

	if len(read.Measured) != 1 || len(read.Mutated) != 0 {
		t.Fatalf("read = %d measurements, %d counts; want the measurements alone", len(read.Measured), len(read.Mutated))
	}
	if err := read.Directories[0].MutantsErr; !errors.Is(err, os.ErrNotExist) {
		t.Errorf("MutantsErr = %v, want the absence of counts this job never writes", err)
	}
}

// A directory holding a scan and no measurements is the expected shape of the
// scan job: it yields its claims, and the measurements' absence is kept as an
// absence.
func TestADirectoryHoldingOnlyAScanIsNotReportedAsUnmeasured(t *testing.T) {
	reader := &recordReader{t: t, scans: map[string]recordRead[Scan]{
		"dir": {value: Scan{Findings: []finding.Finding{recordGosecClaim("svc/lib.go", "sha1.New()")}}},
	}}

	read, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"dir"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}

	if len(read.Measured) != 0 || len(read.Found) != 1 || !read.Scanned {
		t.Fatalf("read = %d measurements, %d findings, scanned %v; want the scan alone",
			len(read.Measured), len(read.Found), read.Scanned)
	}
	if err := read.Directories[0].MeasurementsErr; !errors.Is(err, os.ErrNotExist) {
		t.Errorf("MeasurementsErr = %v, want the absence of measurements this job never writes", err)
	}
}

// A report directory holding neither document yields nothing, and still
// carries why.
func TestAReportDirectoryHoldingNeitherDocumentStillSaysWhy(t *testing.T) {
	read, err := ReadReports(context.Background(), ReadReportsIn{Reader: &recordReader{t: t}, Reports: []string{"dir"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}

	if len(read.Measured) != 0 || read.Scanned {
		t.Fatalf("read = %d measurements, scanned %v; want nothing from an empty directory", len(read.Measured), read.Scanned)
	}
	if read.Directories[0].MeasurementsErr == nil {
		t.Error("a directory that yielded nothing carries no reason")
	}
}

// A directory holding measurements and no scan is no scan, and the scan's
// absence is kept as an absence rather than a document that failed to read.
func TestAReportDirectoryWithNoScanDocumentSaysNothingAboutOne(t *testing.T) {
	reader := &recordReader{t: t, measurements: map[string]recordRead[Measurements]{
		"dir": {value: Measurements{Tree: "deadbeef"}},
	}}

	read, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"dir"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}

	if len(read.Measured) != 1 || read.Scanned {
		t.Fatalf("read = %d measurements, scanned %v; want the measurements alone", len(read.Measured), read.Scanned)
	}
	if err := read.Directories[0].ScanErr; !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ScanErr = %v, want the absence of a scan document that was never written", err)
	}
}

// A document that is there and will not parse is kept as its own error, and
// never read as one that was never written.
//
// The two are opposite answers. An absent document is the shape of a job that
// writes the other one, and says nothing a reader must act on; a document that
// cannot be read is a measurement or a set of counts this recording was meant
// to hold and silently does not.
func TestADocumentThatWillNotParseIsReportedRatherThanReadAsAbsent(t *testing.T) {
	unparseable := func(name string) error {
		return errors.New(name + ": invalid character 'n' looking for beginning of object key string")
	}

	// Measurements beside a scan that will not parse: the directory still
	// yields its components, and the scan's reason is kept beside them.
	reader := &recordReader{t: t,
		measurements: map[string]recordRead[Measurements]{"dir": {value: Measurements{Tree: "deadbeef"}}},
		scans:        map[string]recordRead[Scan]{"dir": {err: unparseable("scan.json")}},
	}
	read, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"dir"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}
	if read.Scanned {
		t.Error("a scan document that could not be read was counted as a scan that ran")
	}
	if len(read.Measured) != 1 {
		t.Fatalf("read = %d measurements, want the measurements still folded", len(read.Measured))
	}
	if err := read.Directories[0].ScanErr; err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("ScanErr = %v, want the parse failure rather than an absence", err)
	}

	// And neither document readable: nothing is yielded, and both reasons are
	// kept.
	reader = &recordReader{t: t,
		measurements: map[string]recordRead[Measurements]{"both": {err: unparseable("measurements.json")}},
		scans:        map[string]recordRead[Scan]{"both": {err: unparseable("scan.json")}},
	}
	read, err = ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"both"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}
	if len(read.Measured) != 0 || read.Scanned {
		t.Fatalf("read = %d measurements, scanned %v; want nothing from two unreadable documents",
			len(read.Measured), read.Scanned)
	}
	d := read.Directories[0]
	for name, err := range map[string]error{"measurements": d.MeasurementsErr, "scan": d.ScanErr} {
		if err == nil || errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s error = %v, want the parse failure rather than an absence", name, err)
		}
	}
}
