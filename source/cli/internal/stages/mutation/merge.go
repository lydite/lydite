package mutationstages

// The stages `lydite mutation merge` runs beside shardstages.ReadShards:
// loading the declaration a complete run covers, reading the counts each shard
// wrote beside its report, folding them, and finding what each component's log
// says the run projected. Each returns what it read as data; which rows a
// fold becomes is the command's to decide.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/shard"
)

// LoadComponentsIn is the root whose declaration applies.
type LoadComponentsIn struct {
	Dir string
}

// LoadComponentsOut is the declaration in force.
type LoadComponentsOut struct {
	File component.File
	// Declared reports whether the declaration names any component at all.
	// Every later stage of the fold runs only when it holds.
	Declared bool
}

// LoadComponents reads the declaration, and nothing else: a fold executes
// nothing from the repository, so it has no configuration to consult.
//
// A declaration naming no component is not an error: Declared is false, and
// what that means for the fold is the caller's to say.
func LoadComponents(_ context.Context, in LoadComponentsIn) (LoadComponentsOut, error) {
	file, err := component.Load(in.Dir)
	if err != nil {
		return LoadComponentsOut{}, err
	}
	return LoadComponentsOut{File: file, Declared: len(file.Components) > 0}, nil
}

// ReadShardCountsIn is every shard ReadShards returned.
type ReadShardCountsIn struct {
	Shards []shard.Shard
}

// ReadShardCountsOut is one ShardCounts per shard, in Shards' order.
type ReadShardCountsOut struct {
	Shards []ShardCounts
}

// ShardCounts is the counts one shard wrote beside its report.
type ShardCounts struct {
	Dir string
	// Counts is the shard's document, the zero document when Read is false.
	Counts mutation.CountsDocument
	// Read reports whether Counts was read.
	Read bool
	// Err is why a document that is there was not read: it would not parse,
	// or names no tree. Nil for a shard that wrote none, and for one whose
	// report was not read, since there is no report to read counts beside.
	Err error
}

// ReadShardCounts reads the counts each shard whose report was read wrote
// beside it.
//
// A shard that wrote none is a shard whose lydite is older than the document,
// which is a run whose counts have to come back out of its prose rather than a
// run that went missing, so it is neither Read nor an Err. A document that is
// there and will not parse is neither either, and is that shard's Err: treated
// as absent, the fold would quietly answer from the prose while the shard's
// row still read `pass`.
//
// It never fails as a whole: every shard's answer is its own.
func ReadShardCounts(_ context.Context, in ReadShardCountsIn) (ReadShardCountsOut, error) {
	out := make([]ShardCounts, 0, len(in.Shards))
	for _, shard := range in.Shards {
		counts := ShardCounts{Dir: shard.Dir}
		if shard.Read {
			switch doc, err := mutation.ReadCounts(shard.Dir); {
			case err == nil:
				counts.Counts, counts.Read = doc, true
			case !errors.Is(err, fs.ErrNotExist):
				counts.Err = err
			}
		}
		out = append(out, counts)
	}
	return ReadShardCountsOut{Shards: out}, nil
}

// FoldShardCountsIn is every shard's counts.
type FoldShardCountsIn struct {
	Shards []ShardCounts
}

// FoldShardCountsOut is the counts folded across the shards.
type FoldShardCountsOut struct {
	// Counts is the fold, the zero document when no shard's counts were read
	// or when they could not be folded.
	Counts mutation.CountsDocument
	// Err is why the counts that were read do not fold into one: the shards
	// disagree about the tree. Nil when they fold, and when there was nothing
	// to fold.
	Err error
}

// FoldShardCounts folds the counts of every shard that wrote them.
//
// A disagreement is returned as Err rather than failing the stage, because it
// is part of what the fold reports: shards that mutated different trees are
// not one run, and the caller says so beside every other reason the shards do
// not fold. No counts at all is not a disagreement — every shard's lydite
// predates the document — and answers with neither counts nor Err.
func FoldShardCounts(_ context.Context, in FoldShardCountsIn) (FoldShardCountsOut, error) {
	var docs []mutation.CountsDocument
	for _, shard := range in.Shards {
		if shard.Read {
			docs = append(docs, shard.Counts)
		}
	}
	if len(docs) == 0 {
		return FoldShardCountsOut{}, nil
	}
	folded, err := mutation.FoldCounts(docs)
	if err != nil {
		return FoldShardCountsOut{Err: err}, nil
	}
	return FoldShardCountsOut{Counts: folded}, nil
}

// ReadProjectionsIn is where the shards' logs are, and whose logs to read.
type ReadProjectionsIn struct {
	// Reports is each shard's report directory, in the order the caller named
	// them.
	Reports []string
	File    component.File
	// LogName is the name a run gives each component's mutation log, inside a
	// directory named for the component. The caller writes that log, so the
	// name is the caller's to say.
	LogName string
}

// ReadProjectionsOut is, per component, the projection its log carries.
type ReadProjectionsOut struct {
	// Projections is keyed by component name, and holds only the components
	// some shard's log carries a projection for.
	Projections map[string]Projection
}

// Projection is the line a run wrote saying what it was about to cost, and
// the shard directory whose log said it.
type Projection struct {
	Dir  string
	Line string
}

// ReadProjections finds, for every declared component, the first shard in
// Reports' order whose log for it carries the projection.
//
// It is what a shard's uploaded directory still says about a component the run
// may have left no row for: the run said what it was about to cost before it
// spent it. The first shard answers because a component is one shard's
// responsibility; two would mean two jobs ran the same work, which the fold
// reports on its own.
func ReadProjections(_ context.Context, in ReadProjectionsIn) (ReadProjectionsOut, error) {
	found := map[string]Projection{}
	for _, c := range in.File.Components {
		for _, dir := range in.Reports {
			if line, ok := shardProjection(dir, c.Name, in.LogName); ok {
				found[c.Name] = Projection{Dir: dir, Line: line}
				break
			}
		}
	}
	return ReadProjectionsOut{Projections: found}, nil
}

// shardProjection reads the projection out of one component's mutation log
// inside a shard's report directory.
//
// The log is found by the layout a run writes — a directory named for the
// component, holding the log named for the command — rather than by walking
// whatever subdirectories the artifact happens to hold, so a lone shard
// extracted straight into the reports directory reads the same as one nested
// under a directory of its own.
//
// Every failure is the same answer, which is that there is no projection to
// quote: a missing directory, a log that cannot be opened, and a log that never
// reached the line are all a fold that says what it said before.
func shardProjection(dir, name, logName string) (string, bool) {
	f, err := os.Open(filepath.Join(dir, name, logName)) // #nosec G304 -- a shard's own report directory, under a directory named for a declared component
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	scan := bufio.NewScanner(f)
	// A suite writes whatever it likes into this log, and a single line longer
	// than the scanner's token limit ends the scan where it stands. The
	// projection is one short line below those, so the limit is raised past the
	// default until a log holding long lines is read through. The ceiling is the
	// whole of the decision: the scanner grows its own buffer to whatever a line
	// needs up to it.
	scan.Buffer(nil, 1024*1024)
	for scan.Scan() {
		if line, ok := costProjectionIn(scan.Text()); ok {
			return line, true
		}
	}
	return "", false
}

// costProjectionIn returns the projection a mutation log carries, if it carries
// one.
//
// The whole line, verbatim, because what a reader is shown is what the run
// itself said — a projection restated in the fold's own words would be the
// fold making a claim about a run it never saw.
func costProjectionIn(line string) (string, bool) {
	line = strings.TrimSpace(line)
	var mutants, workers int
	var budget, ceiling string
	n, err := fmt.Sscanf(line, costProjectionFormat, &mutants, &budget, &workers, &ceiling)
	if err != nil || n != 4 {
		return "", false
	}
	return line, true
}
