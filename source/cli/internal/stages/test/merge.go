package teststages

// The stages `lydite test merge` runs beside shardstages.ReadShards: loading
// the declaration a complete run covers, and reading the measurements each
// shard wrote beside its report. Unlike the stages `lydite test` runs, neither
// returns a ui.Row. Each returns what it found as data, and a failure as the
// error that caused it, unchanged — which rows a fold becomes, and the words
// its refusals are shown in, are the command's to decide.

import (
	"context"
	"errors"
	"io/fs"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/shard"
)

// LoadMergeComponentsIn is the root whose declaration applies.
type LoadMergeComponentsIn struct {
	Dir string
}

// LoadMergeComponentsOut is the declaration in force.
type LoadMergeComponentsOut struct {
	File component.File
	// Declared reports whether the declaration names any component at all.
	// Every later stage of the fold runs only when it holds.
	Declared bool
}

// LoadMergeComponents reads the declaration, and nothing else: a fold executes
// nothing from the repository, and the configuration it composes figures under
// is the command's to read.
//
// A declaration naming no component is not an error: Declared is false, and
// what that means for the fold is the caller's to say.
func LoadMergeComponents(_ context.Context, in LoadMergeComponentsIn) (LoadMergeComponentsOut, error) {
	file, err := component.Load(in.Dir)
	if err != nil {
		return LoadMergeComponentsOut{}, err
	}
	return LoadMergeComponentsOut{File: file, Declared: len(file.Components) > 0}, nil
}

// MeasurementsReader reads the measurements document a shard of `lydite test`
// wrote beside its report.
//
// The document is `lydite test`'s own, in types its composition reaches into,
// so reading it stays with the code that owns it and nothing about its content
// crosses into a stage: what a fold composes from each document read is the
// reader's to keep. A stage learns only whether a directory's document was
// read, and why not.
type MeasurementsReader interface {
	// ReadMeasurements reads the measurements document in dir. A document
	// that is simply not there is an error satisfying
	// errors.Is(err, fs.ErrNotExist); one that is there and will not read is
	// an error that does not, and its text is what a reader of the fold is
	// shown.
	ReadMeasurements(dir string) error
}

// ReadShardMeasurementsIn is every shard ReadShards returned, and what reads
// the measurements beside each.
type ReadShardMeasurementsIn struct {
	Reader MeasurementsReader
	Shards []shard.Shard
}

// ReadShardMeasurementsOut is one ShardMeasurements per shard, in Shards'
// order.
type ReadShardMeasurementsOut struct {
	Shards []ShardMeasurements
}

// ShardMeasurements is what became of reading one shard's measurements.
type ShardMeasurements struct {
	Dir string
	// Read reports whether the shard's measurements document was read, which
	// is whether the reader holds it.
	Read bool
	// Err is why a document that is there was not read: it would not parse,
	// or names no tree. It is the reader's error, unchanged. Nil for a shard
	// that wrote none, and for one whose report was not read, since there is
	// no report to read measurements beside.
	Err error
}

// ReadShardMeasurements reads the measurements each shard whose report was
// read wrote beside it.
//
// A shard run with --no-coverage writes none, which is a run that gated
// nothing rather than a run that went missing, so it is neither Read nor an
// Err. A document that is there and will not read is neither either, and is
// that shard's Err: treated as absent, it would leave that shard's components
// composing nothing while the shard's row still read `pass`.
//
// It never fails as a whole: every shard's answer is its own.
func ReadShardMeasurements(_ context.Context, in ReadShardMeasurementsIn) (ReadShardMeasurementsOut, error) {
	out := make([]ShardMeasurements, 0, len(in.Shards))
	for _, s := range in.Shards {
		m := ShardMeasurements{Dir: s.Dir}
		if s.Read {
			switch err := in.Reader.ReadMeasurements(s.Dir); {
			case err == nil:
				m.Read = true
			case !errors.Is(err, fs.ErrNotExist):
				m.Err = err
			}
		}
		out = append(out, m)
	}
	return ReadShardMeasurementsOut{Shards: out}, nil
}
