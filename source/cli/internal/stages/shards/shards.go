// Package shardstages holds the generic stage every fold reads its shards
// through: one report document per --reports directory, for one command.
// Shared by the commands that fold, so `lydite test merge` and `lydite
// mutation merge` read their shards identically rather than each through a
// copy of its own. Named apart from internal/shard so a flow definition can
// import both without renaming either.
//
// Nothing here decides a row. A shard that could not be read is returned as
// data — its directory and the error — and the caller says what that means in
// its own report.
package shardstages

import (
	"context"

	"lydite/lydite/internal/shard"
)

// ReadShardsIn is the directories to read, and the command whose document
// each holds.
type ReadShardsIn struct {
	// Reports is each shard's report directory, in the order a caller named
	// them.
	Reports []string
	// Command names the document read from each directory: `<command>.json`.
	Command string
}

// ReadShardsOut is every shard, one per directory, in Reports' order.
type ReadShardsOut struct {
	Shards []shard.Shard
}

// ReadShards reads each directory's report document for one command.
//
// One Shard per directory, in the order Reports names them and including
// every directory whose document could not be read, because a fold says what
// it was folded from and a shard that went missing is part of that answer.
// ReadShards itself never fails: a shard it cannot read is that shard's Err,
// not a reason to stop reading the rest.
//
// Each directory is read by shard.Read, which finds the document by name
// alone.
func ReadShards(_ context.Context, in ReadShardsIn) (ReadShardsOut, error) {
	shards := make([]shard.Shard, 0, len(in.Reports))
	for _, dir := range in.Reports {
		shards = append(shards, shard.Read(dir, in.Command))
	}
	return ReadShardsOut{Shards: shards}, nil
}
