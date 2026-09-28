// Package recordflow declares the flow that records what one or more runs
// measured: loading the tree's own declaration, reading and folding the report
// directories, binding the recording to the tree that is checked out, counting
// the scan's findings, binding the mutant counts, composing what the quality
// history is made of, composing the history, deciding the baseline, and the
// one write that lands both on the state branch.
//
// It is a declaration and nothing else. What a recording measured is
// recordstages' to decide, and how a history is composed and landed is
// ledgerstages': neither imports the other, and this declaration is what binds
// one's outputs to the other's inputs. Every value the flow starts from is an
// input its caller supplies — the documents themselves arrive through a
// ReportReader, because each is written by another command in that command's
// own types — so nothing here reads a document, prints, or chooses a report
// row: what the run produced is read back off the Result by the stage names
// below.
package recordflow

import (
	"lydite/lydite/internal/flow"
	ledgerstages "lydite/lydite/internal/stages/ledger"
	recordstages "lydite/lydite/internal/stages/record"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "record"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputDir is the checkout being recorded: where the declaration is read,
	// whose tree the recording binds to, and whose state branch it lands on.
	InputDir = "Dir"
	// InputBranch is the branch the history is filed under, empty to take
	// the one that is checked out.
	InputBranch = "Branch"
	// InputReports is each report directory, in the order it was named.
	InputReports = "Reports"
	// InputReader is the recordstages.ReportReader the documents are read and
	// folded through.
	InputReader = "Reader"
	// InputLangEnabled and InputScannerGates decide which gates a component's
	// finding counts are seeded for.
	InputLangEnabled  = "LangEnabled"
	InputScannerGates = "ScannerGates"
	// InputRestoreToleratedDips anchors a within-tolerance dip back to what
	// the tree already holds.
	InputRestoreToleratedDips = "RestoreToleratedDips"
)

// The names of the flow's stages, in the order they run.
const (
	StageLoadDeclaration     = "load-declaration"
	StageReadReports         = "read-reports"
	StageFoldMeasurements    = "fold-measurements"
	StageBindTree            = "bind-tree"
	StageCountFindings       = "count-findings"
	StageBindMutants         = "bind-mutants"
	StageComposeLedgerInputs = "compose-ledger-inputs"
	StageComposeRecords      = "compose-records"
	StageDecideBaseline      = "decide-baseline"
	StageWriteState          = "write-state"
)

// Params is what one run records.
type Params struct {
	Dir     string
	Branch  string
	Reports []string
	Reader  recordstages.ReportReader

	LangEnabled          recordstages.LangEnabled
	ScannerGates         recordstages.ScannerGates
	RestoreToleratedDips recordstages.RestoreToleratedDips
}

// Inputs are p as the flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDir:                  p.Dir,
		InputBranch:               p.Branch,
		InputReports:              p.Reports,
		InputReader:               p.Reader,
		InputLangEnabled:          p.LangEnabled,
		InputScannerGates:         p.ScannerGates,
		InputRestoreToleratedDips: p.RestoreToleratedDips,
	}
}

// New builds the flow.
//
// The first four stages run unconditionally and fail the run on their error:
// a declaration that cannot be read, a set of directories holding no
// measurements, or a checkout whose tree cannot be resolved leaves nothing a
// recording could be filed against. bind-tree is the last of them, and a
// mismatch is its answer rather than its error, so every stage after it runs
// only when that answer is Bound. That one condition is the only one in the
// flow, and it reads the output of a stage that has no condition of its own
// and fails the run whenever it fails — so a stage reaching it always finds
// bind-tree's output there, and no condition needs another declared before
// it.
//
// compose-ledger-inputs turns what the recording measured into the ledger's
// own vocabulary — each component's scalars, and which finding buckets were
// in scope — and compose-records composes the history from those alone.
// compose-records reads compose-ledger-inputs' output under the same one
// condition and needs no second: whenever that condition holds,
// compose-ledger-inputs held it too and ran, and it fails the run on any error
// of its own, so its output is always there to read.
//
// The history is composed before the baseline is decided, and the two are
// independent: a partial baseline is refused while the history of the same
// run is still appended. compose-records hands write-state a function rather
// than the records, which only gitstate.Write's retry loop ever calls, so each
// attempt composes against the branch it just fetched. write-state is the one
// stage that reaches the branch, landing both in one commit, and its error is
// recorded on the Result rather than failing the run: what a write that never
// landed means — a recording that failed, or a routine push race over history
// alone — depends on what decide-baseline and compose-records said was being
// landed, which is the caller's to read off the Result.
func New() (*flow.Flow, error) {
	reader := flow.FromInput(InputReader)
	dir := flow.FromInput(InputDir)
	declaration := flow.FromStage(StageLoadDeclaration, "Declaration")
	config := flow.FromStage(StageLoadDeclaration, "Config")
	folded := flow.FromStage(StageFoldMeasurements, "Folded")
	bound := flow.FromStage(StageBindTree, "Bound")
	head := flow.FromStage(StageBindTree, "Head")
	found := flow.FromStage(StageReadReports, "Found")

	return flow.New(Name).
		Stage(StageLoadDeclaration, recordstages.LoadDeclaration).
		With("Dir", dir).
		Stage(StageReadReports, recordstages.ReadReports).
		With("Reader", reader).
		With("Reports", flow.FromInput(InputReports)).
		Stage(StageFoldMeasurements, recordstages.FoldMeasurements).
		With("Reader", reader).
		With("Measured", flow.FromStage(StageReadReports, "Measured")).
		Stage(StageBindTree, recordstages.BindTree).
		With("Dir", dir).
		With("Folded", folded).
		Stage(StageCountFindings, recordstages.CountFindings).
		When(bound).
		With("Dir", dir).
		With("Declaration", declaration).
		With("Config", config).
		With("Found", found).
		With("Scanned", flow.FromStage(StageReadReports, "Scanned")).
		With("LangEnabled", flow.FromInput(InputLangEnabled)).
		With("ScannerGates", flow.FromInput(InputScannerGates)).
		Stage(StageBindMutants, recordstages.BindMutants).
		When(bound).
		With("Reader", reader).
		With("Mutated", flow.FromStage(StageReadReports, "Mutated")).
		With("Tree", head).
		Stage(StageComposeLedgerInputs, recordstages.ComposeLedgerInputs).
		When(bound).
		With("Folded", folded).
		With("PerComponent", flow.FromStage(StageCountFindings, "PerComponent")).
		With("Root", flow.FromStage(StageCountFindings, "Root")).
		With("Crashed", flow.FromStage(StageReadReports, "Crashed")).
		With("Mutants", flow.FromStage(StageBindMutants, "Components")).
		Stage(StageComposeRecords, ledgerstages.ComposeRecords).
		When(bound).
		With("Dir", dir).
		With("BranchOverride", flow.FromInput(InputBranch)).
		With("Components", flow.FromStage(StageComposeLedgerInputs, "Components")).
		With("RootFindings", flow.FromStage(StageComposeLedgerInputs, "RootFindings")).
		With("Scope", flow.FromStage(StageComposeLedgerInputs, "Scope")).
		With("Found", found).
		Stage(StageDecideBaseline, recordstages.DecideBaseline).
		When(bound).
		With("Dir", dir).
		With("Declaration", declaration).
		With("Config", config).
		With("Folded", folded).
		With("Head", head).
		With("RestoreToleratedDips", flow.FromInput(InputRestoreToleratedDips)).
		Stage(StageWriteState, ledgerstages.WriteState).
		When(bound).
		With("Dir", dir).
		With("Head", head).
		With("Snapshot", flow.FromStage(StageDecideBaseline, "Snapshot")).
		With("Records", flow.FromStage(StageComposeRecords, "Records")).
		OnError(flow.RecordAndContinue).
		Build()
}
