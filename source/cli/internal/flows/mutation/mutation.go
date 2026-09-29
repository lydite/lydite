// Package mutationflow declares the flows `lydite mutation` and `lydite
// mutation merge` run: one that loads the declaration, provisions each
// component's toolchain, resolves the base, selects and scopes what the
// change touched and runs every selected component's mutants; one that
// records what became of them; and one that folds a matrix of shards into
// what a complete mutation run would have reported.
//
// They are declarations and nothing else. Every stage lives in
// internal/stages/mutation and every value a flow starts from is an input its
// caller supplies, so nothing here reads the environment, prints, or chooses a
// report row: what a run produced is read back off the Result by the stage
// names below.
package mutationflow

import (
	"io"
	"time"

	"lydite/lydite/internal/flow"
	mutationstages "lydite/lydite/internal/stages/mutation"
)

// Name is the name of the flow New builds, which every error it raises for a
// stage carries.
const Name = "mutation"

// RecordName is the name of the flow NewRecord builds.
const RecordName = "mutation-record"

// The keys of the flow.Inputs New's flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputDir is the scan root.
	InputDir = "Dir"
	// InputComponents names the components this run is responsible for, and
	// is empty for every declared component.
	InputComponents = "Components"
	// InputToolchains is the mutationstages.Toolchains each component's
	// toolchain is provisioned through.
	InputToolchains = "Toolchains"
	// InputBaseBranch and InputBaseSHA name the base: a branch to take the
	// merge-base against, or a revision outright.
	InputBaseBranch = "BaseBranch"
	InputBaseSHA    = "BaseSHA"
	// InputOnlyAffected is the bool asking for selection, and InputAffected
	// the mutationstages.AffectedFunc that selects.
	InputOnlyAffected = "OnlyAffected"
	InputAffected     = "Affected"
	// InputShape and InputLifecycle are what a component's declaration
	// implies, and everything around its suite that the caller owns.
	InputShape     = "Shape"
	InputLifecycle = "Lifecycle"
	// InputLimit bounds suite executions in flight across the whole run.
	InputLimit = "Limit"
	// InputTimeout and InputMemory override each mutant's budget and memory
	// bound; zero asks for the derivation from the component's baseline.
	InputTimeout = "Timeout"
	InputMemory  = "Memory"
	// InputDeadline is the time.Time the run stops dispatching at and
	// cancels what is in flight, and zero for a run with none.
	InputDeadline = "Deadline"
	// InputStream mirrors each component's log as it is written.
	InputStream = "Stream"
	// InputDiagnostics is the io.Writer a declaration that matched no mutant
	// is named on.
	InputDiagnostics = "Diagnostics"
	// InputStateDir is the resume state root, and is empty when resume is
	// off. InputFresh asks for what it holds to be ignored, and
	// InputLyditeVersion is the lydite that writes what it records.
	InputStateDir      = "StateDir"
	InputFresh         = "Fresh"
	InputLyditeVersion = "LyditeVersion"
)

// The keys of the flow.Inputs NewRecord's flow is run with.
// RecordParams.Inputs supplies every one of them.
const (
	// InputRecordDir is the scan root, whose HEAD tree the counts are
	// recorded against.
	InputRecordDir = "Dir"
	// InputReportsDir is the run's reports directory, and InputIgnore the
	// func(string) error keeping it out of git.
	InputReportsDir = "ReportsDir"
	InputIgnore     = "Ignore"
	// InputOutcomes is the []mutationstages.ComponentOutcome whose verdict
	// stands.
	InputOutcomes = "Outcomes"
	// InputWarnings is the io.Writer a failure to record is said on.
	InputWarnings = "Warnings"
)

// The names of New's stages, in the order they run.
const (
	StageLoadDeclaration     = "load-declaration"
	StageProvisionToolchains = "provision-toolchains"
	StageResolveBase         = "resolve-base"
	StageSelectAffected      = "select-affected"
	StageScopeChange         = "scope-change"
	StageRunMutants          = "run-mutants"
)

// StageRecordMutants is NewRecord's one stage.
const StageRecordMutants = "record-mutants"

// Params is what one run mutates, and how.
type Params struct {
	Dir          string
	Components   []string
	Toolchains   mutationstages.Toolchains
	BaseBranch   string
	BaseSHA      string
	OnlyAffected bool
	Affected     mutationstages.AffectedFunc
	Shape        mutationstages.Shape
	Lifecycle    mutationstages.Lifecycle
	Limit        int
	Timeout      time.Duration
	Memory       int64
	// Deadline is when the run stops, as an instant rather than a duration so
	// that whatever ran before the flow counts against it.
	Deadline    time.Time
	Stream      bool
	Diagnostics io.Writer
	// StateDir is the resume state root, empty when resume is off.
	StateDir      string
	Fresh         bool
	LyditeVersion string
}

// Inputs are p as New's flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDir:           p.Dir,
		InputComponents:    p.Components,
		InputToolchains:    p.Toolchains,
		InputBaseBranch:    p.BaseBranch,
		InputBaseSHA:       p.BaseSHA,
		InputOnlyAffected:  p.OnlyAffected,
		InputAffected:      p.Affected,
		InputShape:         p.Shape,
		InputLifecycle:     p.Lifecycle,
		InputLimit:         p.Limit,
		InputTimeout:       p.Timeout,
		InputMemory:        p.Memory,
		InputDeadline:      p.Deadline,
		InputStream:        p.Stream,
		InputDiagnostics:   p.Diagnostics,
		InputStateDir:      p.StateDir,
		InputFresh:         p.Fresh,
		InputLyditeVersion: p.LyditeVersion,
	}
}

// RecordParams is what one run made of its mutants, and where it is written.
type RecordParams struct {
	Dir        string
	ReportsDir string
	Ignore     func(dir string) error
	// Outcomes are the outcomes whose verdict stands: after an interrupted
	// run, only those that survived the caller's withdrawal of interrupted
	// failures.
	Outcomes []mutationstages.ComponentOutcome
	Warnings io.Writer
}

// Inputs are p as NewRecord's flow is run with them.
func (p RecordParams) Inputs() flow.Inputs {
	return flow.Inputs{
		InputRecordDir:  p.Dir,
		InputReportsDir: p.ReportsDir,
		InputIgnore:     p.Ignore,
		InputOutcomes:   p.Outcomes,
		InputWarnings:   p.Warnings,
	}
}

// New builds the flow that runs a mutation.
//
// The declaration is loaded first, and every later stage runs only when it
// names a component at all: a repository declaring none has nothing to
// provision, resolve or mutate, and what that means for the report is the
// caller's to say. That condition is the only one any stage declares, so no
// stage ever reads the output of one that did not run.
//
// The base is resolved once, before selection and scoping, so the components
// selected and the lines mutated answer about the same range. Selection runs
// whether or not it is asked for — passing the run's own components through
// when it is not — so scoping and the run read one output either way.
func New() (*flow.Flow, error) {
	declared := flow.FromStage(StageLoadDeclaration, "Declared")
	dir := flow.FromInput(InputDir)
	shape := flow.FromInput(InputShape)
	cfg := flow.FromStage(StageLoadDeclaration, "Config")
	own := flow.FromStage(StageLoadDeclaration, "Own")
	base := flow.FromStage(StageResolveBase, "Base")
	selected := flow.FromStage(StageSelectAffected, "Selected")
	stateDir := flow.FromInput(InputStateDir)

	return flow.New(Name).
		Stage(StageLoadDeclaration, mutationstages.LoadDeclaration).
		With("Dir", dir).
		With("Components", flow.FromInput(InputComponents)).
		Stage(StageProvisionToolchains, mutationstages.ProvisionToolchains).
		When(declared).
		With("Toolchains", flow.FromInput(InputToolchains)).
		With("Dir", dir).
		With("Config", cfg).
		With("Own", own).
		Stage(StageResolveBase, mutationstages.ResolveBase).
		When(declared).
		With("Dir", dir).
		With("BaseBranch", flow.FromInput(InputBaseBranch)).
		With("BaseSHA", flow.FromInput(InputBaseSHA)).
		Stage(StageSelectAffected, mutationstages.SelectAffected).
		When(declared).
		With("Only", flow.FromInput(InputOnlyAffected)).
		With("Affected", flow.FromInput(InputAffected)).
		With("Dir", dir).
		With("File", flow.FromStage(StageLoadDeclaration, "File")).
		With("Own", own).
		With("Base", base).
		Stage(StageScopeChange, mutationstages.ScopeChange).
		When(declared).
		With("Shape", shape).
		With("Dir", dir).
		With("Base", base).
		With("Selected", selected).
		With("StateDir", stateDir).
		With("Diagnostics", flow.FromInput(InputDiagnostics)).
		Stage(StageRunMutants, mutationstages.RunMutants).
		When(declared).
		With("Shape", shape).
		With("Lifecycle", flow.FromInput(InputLifecycle)).
		With("Dir", dir).
		With("Selected", selected).
		With("Config", cfg).
		With("Envs", flow.FromStage(StageProvisionToolchains, "Envs")).
		With("Changed", flow.FromStage(StageScopeChange, "Changed")).
		With("Files", flow.FromStage(StageScopeChange, "Files")).
		With("Limit", flow.FromInput(InputLimit)).
		With("Timeout", flow.FromInput(InputTimeout)).
		With("Memory", flow.FromInput(InputMemory)).
		With("Deadline", flow.FromInput(InputDeadline)).
		With("Stream", flow.FromInput(InputStream)).
		With("Diagnostics", flow.FromInput(InputDiagnostics)).
		With("StateDir", stateDir).
		With("Fresh", flow.FromInput(InputFresh)).
		With("LyditeVersion", flow.FromInput(InputLyditeVersion)).
		With("TreeDigest", flow.FromStage(StageScopeChange, "TreeDigest")).
		Build()
}

// NewRecord builds the flow that records what a run made of its mutants.
//
// A flow of its own rather than a last stage of New's, because it runs on a
// context no interrupt reaches: a run cut short still records the components
// that finished, and a flow stops before any stage once its context is done.
func NewRecord() (*flow.Flow, error) {
	return flow.New(RecordName).
		Stage(StageRecordMutants, mutationstages.RecordMutants).
		With("Dir", flow.FromInput(InputRecordDir)).
		With("ReportsDir", flow.FromInput(InputReportsDir)).
		With("Ignore", flow.FromInput(InputIgnore)).
		With("Components", flow.FromInput(InputOutcomes)).
		With("Warnings", flow.FromInput(InputWarnings)).
		Build()
}
