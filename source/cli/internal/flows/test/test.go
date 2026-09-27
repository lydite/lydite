// Package testflow declares the flow `lydite test` runs: reading the
// declaration and gating it, provisioning each component's toolchain,
// narrowing the run to what a change could have broken, running every selected
// suite under the scheduler, reporting the flaky gate, and measuring and gating
// coverage and CRAP.
//
// It is a declaration and nothing else. Every stage lives in
// internal/stages/test and every value the flow starts from is an input its
// caller supplies, so nothing here reads a flag, prints, or chooses a report
// row: what the run produced is read back off the Result by the stage names
// below, and each stage's rows are appended to the report in stage order.
package testflow

import (
	"io"

	"lydite/lydite/internal/flow"
	teststages "lydite/lydite/internal/stages/test"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "test"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputDir is the scan root, which holds .lydite/.
	InputDir = "Dir"
	// InputComponents is the `--component` list, empty for every declared
	// component.
	InputComponents = "Components"
	// InputBaseBranch is the `--base-branch` override, empty to discover it.
	InputBaseBranch = "BaseBranch"
	// InputAffected is `--affected`.
	InputAffected = "Affected"
	// InputGateFlaky is `--gate-flaky`.
	InputGateFlaky = "GateFlaky"
	// InputGateCoverage is `--gate-coverage`.
	InputGateCoverage = "GateCoverage"
	// InputConcurrency is how many components run at once, bounding this
	// run's suites and a base tree's alike.
	InputConcurrency = "Concurrency"
	// InputStream is `--stream`.
	InputStream = "Stream"
	// InputInstrument is whether each component runs its instrumented
	// variant: false under `--no-coverage`.
	InputInstrument = "Instrument"
	// InputLogs is the teststages.Logs each component's output is written to.
	InputLogs = "Logs"
	// InputStderr is the io.Writer every stage reports to as it goes.
	InputStderr = "Stderr"
)

// The names of the flow's stages, in the order they run.
const (
	StageDeclaration      = "declaration"
	StageToolchains       = "toolchains"
	StageSelectAffected   = "select-affected"
	StagePrepareFlakyGate = "prepare-flaky-gate"
	StageRun              = "run"
	StageFlakyGate        = "flaky-gate"
	StageCoverage         = "coverage"
)

// Params is what one run tests.
type Params struct {
	Dir          string
	Components   []string
	BaseBranch   string
	Affected     bool
	GateFlaky    bool
	GateCoverage bool
	Concurrency  int
	Stream       bool
	Instrument   bool
	Logs         teststages.Logs
	Stderr       io.Writer
}

// Inputs are p as the flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDir:          p.Dir,
		InputComponents:   p.Components,
		InputBaseBranch:   p.BaseBranch,
		InputAffected:     p.Affected,
		InputGateFlaky:    p.GateFlaky,
		InputGateCoverage: p.GateCoverage,
		InputConcurrency:  p.Concurrency,
		InputStream:       p.Stream,
		InputInstrument:   p.Instrument,
		InputLogs:         p.Logs,
		InputStderr:       p.Stderr,
	}
}

// New builds the flow.
//
// No stage is conditioned, and every one runs on every run. Each decides for
// itself when it has nothing to do — selection when it was not asked for or
// nothing is declared, the run when nothing was selected, coverage when
// nothing is declared or nothing was instrumented — because each of those
// cases still contributes rows the report has to hold, and a stage the flow
// skipped would leave its outputs unavailable to every stage after it.
//
// Toolchains are provisioned over the whole responsibility set before
// selection runs, so an `--affected` that cannot resolve its merge-base fails
// after provisioning has reported what it resolved. The flaky gate is prepared
// before the run, carried out inside each component's run, and reported by its
// own stage from the Gate the run hands on (ADR 0062). Every stage fails the
// run on an error: none of them can be recovered from by the stages after it.
func New() (*flow.Flow, error) {
	dir := flow.FromInput(InputDir)
	baseBranch := flow.FromInput(InputBaseBranch)
	affected := flow.FromInput(InputAffected)
	concurrency := flow.FromInput(InputConcurrency)
	instrument := flow.FromInput(InputInstrument)
	logs := flow.FromInput(InputLogs)
	stderr := flow.FromInput(InputStderr)
	cfg := flow.FromStage(StageDeclaration, "Config")
	decl := flow.FromStage(StageDeclaration, "Decl")
	own := flow.FromStage(StageDeclaration, "Own")

	return flow.New(Name).
		Stage(StageDeclaration, teststages.Declaration).
		With("Dir", dir).
		With("Components", flow.FromInput(InputComponents)).
		With("Stderr", stderr).
		Stage(StageToolchains, teststages.Toolchains).
		With("Dir", dir).
		With("Config", cfg).
		With("Own", own).
		With("Stderr", stderr).
		Stage(StageSelectAffected, teststages.SelectAffected).
		With("Dir", dir).
		With("Decl", decl).
		With("Own", own).
		With("Affected", affected).
		With("BaseBranch", baseBranch).
		Stage(StagePrepareFlakyGate, teststages.PrepareFlakyGate).
		With("Dir", dir).
		With("BaseBranch", baseBranch).
		With("Requested", flow.FromInput(InputGateFlaky)).
		Stage(StageRun, teststages.Run).
		With("Dir", dir).
		With("Decl", decl).
		With("Own", own).
		With("Selected", flow.FromStage(StageSelectAffected, "Selected")).
		With("Ordered", flow.FromStage(StageSelectAffected, "Ordered")).
		With("Skipped", flow.FromStage(StageSelectAffected, "Skipped")).
		With("Config", cfg).
		With("Envs", flow.FromStage(StageToolchains, "Envs")).
		With("Concurrency", concurrency).
		With("Stream", flow.FromInput(InputStream)).
		With("Instrument", instrument).
		With("Gate", flow.FromStage(StagePrepareFlakyGate, "Gate")).
		With("Logs", logs).
		Stage(StageFlakyGate, teststages.FlakyGate).
		With("Gate", flow.FromStage(StageRun, "Gate")).
		With("Own", own).
		Stage(StageCoverage, teststages.Coverage).
		With("Dir", dir).
		With("Decl", decl).
		With("Own", own).
		With("Measurements", flow.FromStage(StageRun, "Measurements")).
		With("Config", cfg).
		With("Instrument", instrument).
		With("Gate", flow.FromInput(InputGateCoverage)).
		With("BaseBranch", baseBranch).
		With("Concurrency", concurrency).
		With("Affected", affected).
		With("Narrowed", flow.FromStage(StageDeclaration, "Narrowed")).
		With("Logs", logs).
		With("Stderr", stderr).
		Build()
}
