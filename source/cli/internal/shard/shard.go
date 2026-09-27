// Package shard reads what one shard of a sharded run left behind: the report
// document one command wrote into one report directory.
//
// Nothing here decides a row. A shard whose document could not be read is
// returned as data — its directory and the error — and the caller says what
// that means in its own report.
package shard

import (
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/ui"
)

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

// Read reads one directory's report document for one command.
//
// It never fails as a whole: a document it cannot read is the returned
// Shard's Err, with Read false.
//
// The document is found by name alone. Its own command field is not compared
// against command, so a document at `<command>.json` is that command's
// document whatever it says inside.
func Read(dir, command string) Shard {
	doc, err := readDocument(documentPath(dir, command))
	if err != nil {
		return Shard{Dir: dir, Err: err}
	}
	return Shard{Dir: dir, Document: doc, Read: true}
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
