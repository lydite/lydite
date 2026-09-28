package testflow

import (
	"lydite/lydite/internal/flow"
	shardstages "lydite/lydite/internal/stages/shards"
	teststages "lydite/lydite/internal/stages/test"
)

// MergeName is the name of the flow NewMerge builds.
const MergeName = "test-merge"

// The keys of the flow.Inputs NewMerge's flow is run with. MergeParams.Inputs
// supplies every one of them.
const (
	// InputMergeDir is the root whose declaration says which components a
	// complete run covers.
	InputMergeDir = "Dir"
	// InputReports is each shard's report directory, in the order named.
	InputReports = "Reports"
	// InputReader reads the measurements document beside each shard's
	// report. Its implementation lives with the command that owns that
	// document, so a run supplies it rather than the flow constructing one.
	InputReader = "Reader"
)

// The names of NewMerge's stages, in the order they run.
const (
	StageLoadMergeComponents   = "load-merge-components"
	StageReadShards            = "read-shards"
	StageReadShardMeasurements = "read-shard-measurements"
)

// mergeCommand is the command whose report documents a test fold reads.
const mergeCommand = "test"

// MergeParams is what one fold reads.
type MergeParams struct {
	Dir     string
	Reports []string
	Reader  teststages.MeasurementsReader
}

// Inputs are p as NewMerge's flow is run with them.
func (p MergeParams) Inputs() flow.Inputs {
	return flow.Inputs{
		InputMergeDir: p.Dir,
		InputReports:  p.Reports,
		InputReader:   p.Reader,
	}
}

// NewMerge builds the flow that reads what a matrix of test shards wrote.
//
// The declaration is loaded first, and every later stage runs only when it
// names a component at all: a fold over no component cannot report a shard
// that died, and what that means is the caller's to say. That condition is
// the only one any stage declares, so no stage ever reads the output of one
// that did not run.
//
// Nothing from the repository is executed and nothing is fetched. Each
// shard's report and the measurements document beside it are read and
// returned as they were found; composing them into rows is the caller's.
func NewMerge() (*flow.Flow, error) {
	declared := flow.FromStage(StageLoadMergeComponents, "Declared")

	return flow.New(MergeName).
		Stage(StageLoadMergeComponents, teststages.LoadMergeComponents).
		With("Dir", flow.FromInput(InputMergeDir)).
		Stage(StageReadShards, shardstages.ReadShards).
		When(declared).
		With("Reports", flow.FromInput(InputReports)).
		With("Command", flow.Literal(mergeCommand)).
		Stage(StageReadShardMeasurements, teststages.ReadShardMeasurements).
		When(declared).
		With("Shards", flow.FromStage(StageReadShards, "Shards")).
		With("Reader", flow.FromInput(InputReader)).
		Build()
}
