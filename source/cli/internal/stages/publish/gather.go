// Package publishstages holds the stages that render the standing
// pull-request comment from the report documents one or more runs wrote:
// gathering every report directory, building the comment from what they hold,
// and writing it where the caller asked. Named after the stage-package
// convention, apart from the publish flow definition, so a flow definition can
// import both without renaming either.
//
// Every stage is a plain function of its own In. Reading a report directory
// and reading a row's log are the caller's own readers, passed in as
// ReadDocuments and ReadLog, so nothing here knows where a document or a log
// is kept.
package publishstages

import (
	"context"
	"errors"
	"os"

	"lydite/lydite/internal/ui"
)

// ReadDocuments reads every report document one report directory holds.
type ReadDocuments func(dir string) ([]ui.Document, error)

// ReportDir is one report directory the comment is rendered from: the
// documents it holds, or why it contributed none.
type ReportDir struct {
	Dir       string
	Documents []ui.Document
	// Missing is the reader-facing reason Dir contributed nothing — it is
	// absent, unreadable, or holds no document. A ReportDir with no
	// Documents is one that contributed nothing, whatever Missing says.
	Missing string
}

// GatherIn names the directories GatherReports reads, and the reader it reads
// them with.
type GatherIn struct {
	Dirs          []string
	ReadDocuments ReadDocuments
}

// GatherOut is every directory GatherReports read, in the order it was named.
type GatherOut struct {
	Gathered []ReportDir
}

// GatherReports reads every named directory.
//
// A directory that is absent, unreadable, or holds no document is content,
// not an error: it comes back carrying the reason, so the comment can render
// it as an unmeasured section naming what was missing — never omit it. A
// section that quietly disappears is indistinguishable from a concern that
// passed, which is the wardnet#957 failure: a pull request read green while
// the gate that would have failed it had never run.
func GatherReports(_ context.Context, in GatherIn) (GatherOut, error) {
	var out GatherOut
	for _, dir := range in.Dirs {
		docs, err := in.ReadDocuments(dir)
		switch {
		case err != nil:
			out.Gathered = append(out.Gathered, ReportDir{Dir: dir, Missing: reason(err)})
		case len(docs) == 0:
			out.Gathered = append(out.Gathered, ReportDir{Dir: dir, Missing: "holds no report document"})
		default:
			out.Gathered = append(out.Gathered, ReportDir{Dir: dir, Documents: docs})
		}
	}
	return out, nil
}

// reason turns a directory that could not be read into something a reader can
// act on, rather than a Go error string with a path repeated in it.
func reason(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "no such directory, so nothing from it is in this comment"
	}
	return err.Error()
}
