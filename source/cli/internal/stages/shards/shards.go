// Package shardstages holds the generic stage every fold reads its shards
// through: one report document per --reports directory, for one command.
// Named apart from the commands that fold, so `lydite test merge` and `lydite
// mutation merge` read their shards identically rather than each through a
// copy of its own.
//
// Nothing here decides a row. A shard that could not be read is returned as
// data — its directory and the error — and the caller says what that means in
// its own report.
package shardstages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/ui"
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
	Shards []Shard
}

// Shard is one directory, and what was read out of it.
type Shard struct {
	Dir string
	// Document is the shard's report, the zero Document when Read is false.
	Document ui.Document
	// Read reports whether Document was read.
	Read bool
	// Err is why Document was not read: the document is absent, unreadable,
	// or not a lydite report. Nil when Read is true.
	Err error
}

// ReadShards reads each directory's report document for one command.
//
// One Shard per directory, in the order Reports names them and including
// every directory whose document could not be read, because a fold says what
// it was folded from and a shard that went missing is part of that answer.
// ReadShards itself never fails: a shard it cannot read is that shard's Err,
// not a reason to stop reading the rest.
//
// The document is found by name alone. Its own command field is not compared
// against Command, so a document at `<command>.json` is that command's
// document whatever it says inside.
func ReadShards(_ context.Context, in ReadShardsIn) (ReadShardsOut, error) {
	shards := make([]Shard, 0, len(in.Reports))
	for _, dir := range in.Reports {
		doc, err := readDocument(documentPath(dir, in.Command))
		if err != nil {
			shards = append(shards, Shard{Dir: dir, Err: err})
			continue
		}
		shards = append(shards, Shard{Dir: dir, Document: doc, Read: true})
	}
	return ReadShardsOut{Shards: shards}, nil
}

// documentPath is where one command's report document lives inside a report
// directory: a file at its top, named after the command.
func documentPath(dir, command string) string {
	return filepath.Join(dir, command+".json")
}

// readDocument decodes the report document at path, naming the path in any
// parse error.
func readDocument(path string) (ui.Document, error) {
	f, err := os.Open(path) // #nosec G304 -- path is a document inside a report directory the caller named
	if err != nil {
		return ui.Document{}, err
	}
	defer func() { _ = f.Close() }()
	doc, err := ui.ReadDocument(f)
	if err != nil {
		return ui.Document{}, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}
