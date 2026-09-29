// Package releaseflow declares the flow that checks a tag being released:
// resolving which tag that is, finding the release before it, reading the
// commits the range carries, and judging whether any declared break lands on
// a bump that admits one.
//
// It is a declaration and nothing else. Every stage lives in its own package
// and every value the flow starts from is an input its caller supplies, so
// nothing here reads the environment, prints, or chooses a report row: what
// the run produced is read back off the Result by the stage names below.
package releaseflow

import (
	"lydite/lydite/internal/flow"
	releasestages "lydite/lydite/internal/stages/release"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "release"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputDir is the checkout the tag and its range are resolved over.
	InputDir = "Dir"
	// InputTag is the tag a caller passed explicitly.
	InputTag = "Tag"
	// InputRefType and InputRefName are the type and short name of the ref
	// the run was triggered by.
	InputRefType = "RefType"
	InputRefName = "RefName"
)

// The names of the flow's stages, in the order they run.
const (
	StageResolveTag  = "resolve-tag"
	StagePreviousTag = "previous-tag"
	StageReadCommits = "read-commits"
	StageJudge       = "judge"
)

// Params is what one run checks a release with.
type Params struct {
	Dir     string
	Tag     string
	RefType string
	RefName string
}

// Inputs are p as the flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDir:     p.Dir,
		InputTag:     p.Tag,
		InputRefType: p.RefType,
		InputRefName: p.RefName,
	}
}

// New builds the flow.
//
// resolve-tag settles which tag is being released; previous-tag finds the
// release its range starts from, unconditionally — a run with nothing to
// check still needs to know that. read-commits and judge both run only when
// previous-tag found one: a first release has no range to read commits from
// or judge, and First is previous-tag's own output, so the condition that
// implies it ran is declared on each before the one reading it.
func New() (*flow.Flow, error) {
	first := flow.FromStage(StagePreviousTag, "First")

	return flow.New(Name).
		Stage(StageResolveTag, releasestages.ResolveTag).
		With("Dir", flow.FromInput(InputDir)).
		With("Flag", flow.FromInput(InputTag)).
		With("RefType", flow.FromInput(InputRefType)).
		With("RefName", flow.FromInput(InputRefName)).
		Stage(StagePreviousTag, releasestages.PreviousTag).
		With("Dir", flow.FromInput(InputDir)).
		With("Tag", flow.FromStage(StageResolveTag, "Tag")).
		Stage(StageReadCommits, releasestages.ReadCommits).
		Unless(first).
		With("Dir", flow.FromInput(InputDir)).
		With("Previous", flow.FromStage(StagePreviousTag, "Previous")).
		Stage(StageJudge, releasestages.Judge).
		Unless(first).
		With("Previous", flow.FromStage(StagePreviousTag, "Previous")).
		With("Tag", flow.FromStage(StageResolveTag, "Tag")).
		With("Messages", flow.FromStage(StageReadCommits, "Messages")).
		Build()
}
