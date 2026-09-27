// Package scanflow declares the flow that answers `lydite scan`: reading the
// configuration and the component declaration, resolving the diff base a
// finding is anchored against, provisioning each scanned component's
// toolchain, planning and running its language checks and licence gate, and
// running the two root-scoped scanners.
//
// It is a declaration and nothing else. Every stage lives in its own package
// and every value the flow starts from is an input its caller supplies, so
// nothing here reads the environment, prints, or chooses a report row: what
// the run produced is read back off the Result by the stage names below.
package scanflow

import (
	"io"

	"lydite/lydite/internal/flow"
	scanstages "lydite/lydite/internal/stages/scan"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "scan"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputDir is the scan root: where the configuration and the component
	// declaration are read, where git is run, and where every component's
	// directory is resolved from.
	InputDir = "Dir"
	// InputDiffBase and InputBaseBranch resolve the commit a finding is
	// anchored against.
	InputDiffBase   = "DiffBase"
	InputBaseBranch = "BaseBranch"
	// InputToolchains is what provisions each scanned component's language
	// toolchain.
	InputToolchains = "Toolchains"
	// InputEnvironment composes a component's declared environment onto its
	// provisioned toolchain, and reports what a declaration alone
	// contributed.
	InputEnvironment = "Environment"
	// InputDiagnostics is where lydite's own warnings are written, at the
	// moment they arise, so they interleave with a check's own streamed
	// output in the order the two arise.
	InputDiagnostics = "Diagnostics"
	// InputSemgrepAppToken reports whether SEMGREP_APP_TOKEN is set.
	InputSemgrepAppToken = "SemgrepAppToken"
)

// The names of the flow's stages, in the order they run.
const (
	StageLoadConfig          = "load-config"
	StageLoadComponents      = "load-components"
	StageResolveDiffBase     = "resolve-diff-base"
	StageReadChangedLines    = "read-changed-lines"
	StageProvisionToolchains = "provision-toolchains"
	StageWarnUnscanned       = "warn-unscanned"
	StagePlanComponents      = "plan-components"
	StageRunChecks           = "run-checks"
	StageGateLicences        = "gate-licences"
	StageSemgrep             = "semgrep"
	StageSecrets             = "secrets"
)

// Params is what one run scans with.
type Params struct {
	Dir         string
	DiffBase    string
	BaseBranch  string
	Toolchains  scanstages.Toolchains
	Environment scanstages.Environment
	// Diagnostics is where lydite's own warnings, and a scanner's, are
	// written as they arise.
	Diagnostics io.Writer
	// SemgrepAppToken reports whether SEMGREP_APP_TOKEN is set. Read by the
	// caller: no stage reads the process environment.
	SemgrepAppToken bool
}

// Inputs are p as the flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDir:             p.Dir,
		InputDiffBase:        p.DiffBase,
		InputBaseBranch:      p.BaseBranch,
		InputToolchains:      p.Toolchains,
		InputEnvironment:     p.Environment,
		InputDiagnostics:     p.Diagnostics,
		InputSemgrepAppToken: p.SemgrepAppToken,
	}
}

// New builds the flow.
//
// load-config and load-components read no other stage's output, so both run
// first and in either order; here load-config runs first because semgrep and
// secrets condition on its flat SemgrepEnabled and SecretsEnabled fields.
// resolve-diff-base and read-changed-lines follow, so every later stage that
// anchors a claim or scopes a comparison to the change reads an already
// resolved base. provision-toolchains needs the declaration and the
// configuration, so it runs after both; plan-components needs the
// toolchains provision resolved, so it runs after that; run-checks and
// gate-licences both need the plan, so they run after it, in the order the
// render walk reads them.
//
// semgrep and secrets each condition on load-config's own SemgrepEnabled and
// SecretsEnabled, with no preceding guard needed: load-config is declared
// first, with no condition of its own, under the default FailFlow. By the
// time either condition is evaluated, load-config has therefore already
// either succeeded — making its output available — or its FailFlow error
// already stopped the run before semgrep or secrets was ever reached. The
// ordering subtlety that requires a preceding guard applies to a condition
// reading a stage that itself runs conditionally; load-config runs
// unconditionally, so there is no such stage to guard against here.
func New() (*flow.Flow, error) {
	file := flow.FromStage(StageLoadComponents, "File")
	cfg := flow.FromStage(StageLoadConfig, "Config")
	baseSHA := flow.FromStage(StageResolveDiffBase, "SHA")
	changed := flow.FromStage(StageReadChangedLines, "Changed")
	plan := flow.FromStage(StagePlanComponents, "Plan")

	return flow.New(Name).
		Stage(StageLoadConfig, scanstages.LoadConfig).
		With("Dir", flow.FromInput(InputDir)).
		Stage(StageLoadComponents, scanstages.LoadComponents).
		With("Dir", flow.FromInput(InputDir)).
		Stage(StageResolveDiffBase, scanstages.ResolveDiffBase).
		With("Dir", flow.FromInput(InputDir)).
		With("DiffBase", flow.FromInput(InputDiffBase)).
		With("BaseBranch", flow.FromInput(InputBaseBranch)).
		Stage(StageReadChangedLines, scanstages.ReadChangedLines).
		With("Dir", flow.FromInput(InputDir)).
		With("BaseSHA", baseSHA).
		Stage(StageProvisionToolchains, scanstages.ProvisionToolchains).
		With("Dir", flow.FromInput(InputDir)).
		With("File", file).
		With("Config", cfg).
		With("Toolchains", flow.FromInput(InputToolchains)).
		Stage(StageWarnUnscanned, scanstages.WarnUnscanned).
		With("Dir", flow.FromInput(InputDir)).
		With("File", file).
		With("Config", cfg).
		With("Diagnostics", flow.FromInput(InputDiagnostics)).
		Stage(StagePlanComponents, scanstages.PlanComponents).
		With("Dir", flow.FromInput(InputDir)).
		With("File", file).
		With("Config", cfg).
		With("Envs", flow.FromStage(StageProvisionToolchains, "Envs")).
		With("Environment", flow.FromInput(InputEnvironment)).
		Stage(StageRunChecks, scanstages.RunChecks).
		With("Plan", plan).
		With("Changed", changed).
		With("Environment", flow.FromInput(InputEnvironment)).
		With("Diagnostics", flow.FromInput(InputDiagnostics)).
		Stage(StageGateLicences, scanstages.GateLicences).
		With("Dir", flow.FromInput(InputDir)).
		With("BaseSHA", baseSHA).
		With("Plan", plan).
		With("Config", cfg).
		With("Changed", changed).
		Stage(StageSemgrep, scanstages.Semgrep).
		When(flow.FromStage(StageLoadConfig, "SemgrepEnabled")).
		With("Dir", flow.FromInput(InputDir)).
		With("BaseSHA", baseSHA).
		With("SemgrepAppToken", flow.FromInput(InputSemgrepAppToken)).
		With("Config", flow.FromStage(StageLoadConfig, "SemgrepConfig")).
		With("Changed", changed).
		With("Diagnostics", flow.FromInput(InputDiagnostics)).
		Stage(StageSecrets, scanstages.Secrets).
		When(flow.FromStage(StageLoadConfig, "SecretsEnabled")).
		With("Dir", flow.FromInput(InputDir)).
		With("Changed", changed).
		Build()
}
