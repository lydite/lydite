package teststages

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/shard"
)

// shardReader is a MeasurementsReader answering from a fixed per-directory
// table, and recording every directory it was asked about. A directory the
// table does not name holds no document at all.
type shardReader struct {
	errs  map[string]error
	asked []string
}

func (r *shardReader) ReadMeasurements(dir string) error {
	r.asked = append(r.asked, dir)
	if err, ok := r.errs[dir]; ok {
		return err
	}
	return &fs.PathError{Op: "open", Path: dir + "/measurements.json", Err: fs.ErrNotExist}
}

// readMeasured runs ReadShardMeasurements and fails the test on an error.
func readMeasured(t *testing.T, reader MeasurementsReader, shards ...shard.Shard) []ShardMeasurements {
	t.Helper()
	out, err := ReadShardMeasurements(context.Background(), ReadShardMeasurementsIn{Reader: reader, Shards: shards})
	if err != nil {
		t.Fatalf("ReadShardMeasurements: %v", err)
	}
	return out.Shards
}

// A fold over no component cannot report a shard that died. The stage says so
// as data rather than as an error, so the words of the refusal stay the
// command's.
func TestAMergeOverADeclarationNamingNoComponentIsNotDeclared(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName, "components: []\n")
	out, err := LoadMergeComponents(context.Background(), LoadMergeComponentsIn{Dir: root})
	if err != nil {
		t.Fatalf("LoadMergeComponents: %v", err)
	}
	if out.Declared {
		t.Errorf("Declared = true over an empty declaration")
	}
	if len(out.File.Components) != 0 {
		t.Errorf("File.Components = %+v, want none", out.File.Components)
	}
}

func TestAMergeOverADeclarationNamingAComponentIsDeclared(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: api\n    dir: api\n    runner: go-test\n"+
			"  - name: web\n    dir: web\n    runner: vitest\n")
	write(t, root, "api/.keep", "")
	write(t, root, "web/.keep", "")
	out, err := LoadMergeComponents(context.Background(), LoadMergeComponentsIn{Dir: root})
	if err != nil {
		t.Fatalf("LoadMergeComponents: %v", err)
	}
	if !out.Declared {
		t.Fatal("Declared = false over a declaration naming two components")
	}
	var names []string
	for _, c := range out.File.Components {
		names = append(names, c.Name)
	}
	if want := []string{"api", "web"}; !reflect.DeepEqual(names, want) {
		t.Errorf("components = %v, want %v in declaration order", names, want)
	}
}

// A declaration that will not load fails the stage with the loader's own
// error, rather than reading as a declaration naming nothing.
func TestAMergeOverADeclarationThatWillNotLoadFailsTheStage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName, "components: [\n")
	want := func() error { _, err := component.Load(root); return err }()
	if want == nil {
		t.Fatal("component.Load accepted a malformed declaration")
	}
	_, err := LoadMergeComponents(context.Background(), LoadMergeComponentsIn{Dir: root})
	if err == nil || err.Error() != want.Error() {
		t.Errorf("err = %v, want the loader's own %q", err, want)
	}
}

// A shard run with --no-coverage writes no measurements, which is a run that
// gated nothing rather than one that went missing: it is neither read nor an
// error.
func TestAShardThatWroteNoMeasurementsIsNeitherReadNorAnError(t *testing.T) {
	t.Parallel()
	got := readMeasured(t, &shardReader{}, shard.Shard{Dir: "a", Read: true})
	if want := []ShardMeasurements{{Dir: "a"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("shards = %+v, want %+v", got, want)
	}
}

func TestAShardsMeasurementsThatReadAreRead(t *testing.T) {
	t.Parallel()
	reader := &shardReader{errs: map[string]error{"a": nil}}
	got := readMeasured(t, reader, shard.Shard{Dir: "a", Read: true})
	if want := []ShardMeasurements{{Dir: "a", Read: true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("shards = %+v, want %+v", got, want)
	}
}

// A document that is there and will not read is that shard's own error, and
// the reader's error itself: its text is what the shard's row shows, so it
// crosses the stage unwrapped.
func TestAShardsMeasurementsThatWillNotReadAreItsOwnError(t *testing.T) {
	t.Parallel()
	bad := errors.New("a/measurements.json: invalid character '}' looking for beginning of value")
	reader := &shardReader{errs: map[string]error{"a": bad}}
	got := readMeasured(t, reader, shard.Shard{Dir: "a", Read: true})
	if len(got) != 1 {
		t.Fatalf("shards = %+v, want one", got)
	}
	if got[0].Read {
		t.Errorf("Read = true for a document that would not read")
	}
	if got[0].Err != bad {
		t.Errorf("Err = %v, want the reader's own error unchanged", got[0].Err)
	}
}

// An error that wraps a missing file is still a missing file, whatever
// decorates it.
func TestAWrappedMissingDocumentIsStillNoDocument(t *testing.T) {
	t.Parallel()
	reader := &shardReader{errs: map[string]error{"a": errors.Join(errors.New("reading a"), fs.ErrNotExist)}}
	got := readMeasured(t, reader, shard.Shard{Dir: "a", Read: true})
	if want := []ShardMeasurements{{Dir: "a"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("shards = %+v, want %+v", got, want)
	}
}

// A shard whose report did not read has no report to read measurements
// beside, so its directory is never handed to the reader — its row already
// says why it failed.
func TestAShardWhoseReportDidNotReadIsNotReadForMeasurements(t *testing.T) {
	t.Parallel()
	reader := &shardReader{errs: map[string]error{"a": nil}}
	got := readMeasured(t, reader, shard.Shard{Dir: "a", Err: errors.New("no test report")})
	if want := []ShardMeasurements{{Dir: "a"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("shards = %+v, want %+v", got, want)
	}
	if len(reader.asked) != 0 {
		t.Errorf("reader asked about %v, want nothing", reader.asked)
	}
}

// Every shard's answer is its own, in the order the shards arrived: one that
// will not read fails nothing beside it, and a directory named twice is
// answered twice.
func TestEveryShardsMeasurementsAreItsOwnInShardOrder(t *testing.T) {
	t.Parallel()
	bad := errors.New("c/measurements.json: names no tree, so there is nothing it can be recorded against")
	reader := &shardReader{errs: map[string]error{"a": nil, "c": bad}}
	got := readMeasured(t, reader,
		shard.Shard{Dir: "a", Read: true},
		shard.Shard{Dir: "b", Read: true},
		shard.Shard{Dir: "c", Read: true},
		shard.Shard{Dir: "d", Err: errors.New("no test report")},
		shard.Shard{Dir: "a", Read: true},
	)
	want := []ShardMeasurements{
		{Dir: "a", Read: true},
		{Dir: "b"},
		{Dir: "c", Err: bad},
		{Dir: "d"},
		{Dir: "a", Read: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shards = %+v, want %+v", got, want)
	}
	if wantAsked := []string{"a", "b", "c", "a"}; !reflect.DeepEqual(reader.asked, wantAsked) {
		t.Errorf("reader asked about %v, want %v", reader.asked, wantAsked)
	}
}

// No shard at all is no answer, not a failure.
func TestNoShardsReadsNothing(t *testing.T) {
	t.Parallel()
	got := readMeasured(t, &shardReader{})
	if len(got) != 0 {
		t.Errorf("shards = %+v, want none", got)
	}
}
