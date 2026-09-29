package testflow

import (
	"lydite/lydite/internal/flow"
	teststages "lydite/lydite/internal/stages/test"
)

// PlanName is the name of the flow NewPlan builds.
const PlanName = "test-plan"

// The keys of the flow.Inputs NewPlan's flow is run with. PlanParams.Inputs
// supplies it.
const (
	// InputPlanDir is the scan root whose declaration the matrix covers.
	InputPlanDir = "Dir"
)

// The names of NewPlan's stages, in the order they run.
const (
	StageLoadPlanComponents = "load-plan-components"
	StageGroupShards        = "group-shards"
)

// PlanParams is what one plan groups.
type PlanParams struct {
	Dir string
}

// Inputs are p as NewPlan's flow is run with them.
func (p PlanParams) Inputs() flow.Inputs {
	return flow.Inputs{
		InputPlanDir: p.Dir,
	}
}

// NewPlan builds the flow that groups a declaration's components into
// shards.
//
// The declaration is loaded first, and grouping runs only when it names a
// component at all: a declaration with nothing in it has nothing to group,
// and what that means for the matrix is the caller's to say.
//
// Nothing is fetched and no suite runs; each compose file is read with no
// container runtime, so grouping depends on nothing but the declaration and
// the files beside it.
func NewPlan() (*flow.Flow, error) {
	declared := flow.FromStage(StageLoadPlanComponents, "Declared")

	return flow.New(PlanName).
		Stage(StageLoadPlanComponents, teststages.LoadPlanComponents).
		With("Dir", flow.FromInput(InputPlanDir)).
		Stage(StageGroupShards, teststages.GroupShards).
		When(declared).
		With("Dir", flow.FromInput(InputPlanDir)).
		With("File", flow.FromStage(StageLoadPlanComponents, "File")).
		Build()
}
