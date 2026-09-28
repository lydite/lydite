// Package publishflow declares the flow that renders the standing
// pull-request comment from the report documents one or more runs wrote:
// gathering every named report directory, building the comment from what
// they hold, and writing it where the caller asked.
//
// It is a declaration and nothing else. Every stage lives in its own package
// and every value the flow starts from is an input its caller supplies, so
// nothing here reads the environment, prints, or chooses a report row: what
// the run produced is read back off the Result by the stage names below.
package publishflow

import (
	"io"

	"lydite/lydite/internal/flow"
	publishstages "lydite/lydite/internal/stages/publish"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "publish"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputDirs names the report directories gather-reports reads, in order.
	InputDirs = "Dirs"
	// InputExpect names the commands the run was supposed to produce a
	// report for, so a concern that never ran still renders as unmeasured.
	InputExpect = "Expect"
	// InputBase is the commit the change was measured against, for the
	// footer.
	InputBase = "Base"
	// InputVersion is lydite's own version, for the footer.
	InputVersion = "Version"
	// InputReadDocuments is the reader gather-reports reads every directory
	// with.
	InputReadDocuments = "ReadDocuments"
	// InputReadLog is the reader build-comment reads a failing row's log
	// with, when the row carries no detail of its own.
	InputReadLog = "ReadLog"
	// InputTailLines bounds how many unanchored claims one row quotes.
	InputTailLines = "TailLines"
	// InputOut is where the rendered comment is written: "-" for Stdout, or
	// a path.
	InputOut = "Out"
	// InputStdout is where write-comment writes the comment when Out is
	// "-".
	InputStdout = "Stdout"
)

// The names of the flow's stages, in the order they run.
const (
	StageGatherReports = "gather-reports"
	StageBuildComment  = "build-comment"
	StageWriteComment  = "write-comment"
)

// Params is what one run renders a comment from.
type Params struct {
	Dirs          []string
	Expect        []string
	Base          string
	Version       string
	ReadDocuments publishstages.ReadDocuments
	ReadLog       publishstages.ReadLog
	TailLines     int
	Out           string
	Stdout        io.Writer
}

// Inputs are p as the flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDirs:          p.Dirs,
		InputExpect:        p.Expect,
		InputBase:          p.Base,
		InputVersion:       p.Version,
		InputReadDocuments: p.ReadDocuments,
		InputReadLog:       p.ReadLog,
		InputTailLines:     p.TailLines,
		InputOut:           p.Out,
		InputStdout:        p.Stdout,
	}
}

// New builds the flow.
//
// Three stages, one responsibility each: gather-reports reads every named
// directory into its documents or the reason it held none; build-comment
// folds what gather-reports read into one rendered ui.Comment, reading a
// failing row's log through its own injected reader; write-comment puts that
// comment where the caller asked. No stage is conditioned on another's
// output, so none needs a guard beyond Build's own binding checks.
func New() (*flow.Flow, error) {
	return flow.New(Name).
		Stage(StageGatherReports, publishstages.GatherReports).
		With("Dirs", flow.FromInput(InputDirs)).
		With("ReadDocuments", flow.FromInput(InputReadDocuments)).
		Stage(StageBuildComment, publishstages.BuildComment).
		With("Gathered", flow.FromStage(StageGatherReports, "Gathered")).
		With("Expect", flow.FromInput(InputExpect)).
		With("Base", flow.FromInput(InputBase)).
		With("Version", flow.FromInput(InputVersion)).
		With("ReadLog", flow.FromInput(InputReadLog)).
		With("TailLines", flow.FromInput(InputTailLines)).
		Stage(StageWriteComment, publishstages.WriteComment).
		With("Comment", flow.FromStage(StageBuildComment, "Comment")).
		With("Out", flow.FromInput(InputOut)).
		With("Stdout", flow.FromInput(InputStdout)).
		Build()
}
