package mutationflow

import (
	"lydite/lydite/internal/flow"
	mutationstages "lydite/lydite/internal/stages/mutation"
	shardstages "lydite/lydite/internal/stages/shards"
)

// MergeName is the name of the flow NewMerge builds.
const MergeName = "mutation-merge"

// The keys of the flow.Inputs NewMerge's flow is run with. MergeParams.Inputs
// supplies every one of them.
const (
	// InputMergeDir is the root whose declaration says which components a
	// complete run covers.
	InputMergeDir = "Dir"
	// InputReports is each shard's report directory, in the order named.
	InputReports = "Reports"
	// InputLogName is the name a run gives each component's mutation log.
	InputLogName = "LogName"
)

// The names of NewMerge's stages, in the order they run.
const (
	StageLoadComponents  = "load-components"
	StageReadShards      = "read-shards"
	StageReadShardCounts = "read-shard-counts"
	StageFoldShardCounts = "fold-shard-counts"
	StageReadProjections = "read-projections"
)

// mergeCommand is the command whose documents a mutation fold reads.
const mergeCommand = "mutation"

// MergeParams is what one fold reads.
type MergeParams struct {
	Dir     string
	Reports []string
	// LogName is the name the caller's own runs give each component's
	// mutation log, which is where a component's projection is read from.
	LogName string
}

// Inputs are p as NewMerge's flow is run with them.
func (p MergeParams) Inputs() flow.Inputs {
	return flow.Inputs{
		InputMergeDir: p.Dir,
		InputReports:  p.Reports,
		InputLogName:  p.LogName,
	}
}

// NewMerge builds the flow that reads what a matrix of mutation shards wrote.
//
// The declaration is loaded first, and every later stage runs only when it
// names a component at all: a fold over no component cannot report a shard
// that died, and what that means is the caller's to say. That condition is
// the only one any stage declares, so no stage ever reads the output of one
// that did not run.
//
// Nothing from the repository is executed and nothing is fetched. The shards'
// documents, the counts beside them and the projections in their logs are
// read and returned as they were found; composing them into one report is the
// caller's.
func NewMerge() (*flow.Flow, error) {
	declared := flow.FromStage(StageLoadComponents, "Declared")
	reports := flow.FromInput(InputReports)

	return flow.New(MergeName).
		Stage(StageLoadComponents, mutationstages.LoadComponents).
		With("Dir", flow.FromInput(InputMergeDir)).
		Stage(StageReadShards, shardstages.ReadShards).
		When(declared).
		With("Reports", reports).
		With("Command", flow.Literal(mergeCommand)).
		Stage(StageReadShardCounts, mutationstages.ReadShardCounts).
		When(declared).
		With("Shards", flow.FromStage(StageReadShards, "Shards")).
		Stage(StageFoldShardCounts, mutationstages.FoldShardCounts).
		When(declared).
		With("Shards", flow.FromStage(StageReadShardCounts, "Shards")).
		Stage(StageReadProjections, mutationstages.ReadProjections).
		When(declared).
		With("Reports", reports).
		With("File", flow.FromStage(StageLoadComponents, "File")).
		With("LogName", flow.FromInput(InputLogName)).
		Build()
}
