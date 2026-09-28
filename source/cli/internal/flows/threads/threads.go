// Package threadsflow declares the flow that answers `lydite threads`:
// establishing the run's trust and the repository it acts on, resolving the
// pull request live, reading the findings a run made, listing the threads
// already standing, planning the delta between them, writing that plan down
// and — when asked — applying it.
//
// It is a declaration and nothing else. Every stage lives in its own package
// and every value the flow starts from is an input its caller supplies, so
// nothing here reads the environment, prints, or chooses a report row: what
// the run produced is read back off the Result by the stage names below.
package threadsflow

import (
	"lydite/lydite/internal/flow"
	scmstages "lydite/lydite/internal/stages/scm"
	threadsstages "lydite/lydite/internal/stages/threads"
	truststages "lydite/lydite/internal/stages/trust"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "threads"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputReports are the report directories the findings that become
	// threads are read from.
	InputReports = "Reports"
	// InputReader reads one report directory's findings.
	InputReader = "Reader"
	// InputOpsPath is where the reconciled plan is written.
	InputOpsPath = "OpsPath"
	// InputEventPath is the pull request's event payload.
	InputEventPath = "EventPath"
	// InputApply chooses applying the plan — taking down, answering and
	// opening threads — over merely planning and writing it down.
	InputApply = "Apply"
)

// The names of the flow's stages, in the order they run.
const (
	StageInitTrust       = "init-trust"
	StageInitSCM         = "init-scm"
	StageLoadPullRequest = "load-pull-request"
	StageReadFindings    = "read-findings"
	StageListThreads     = "list-threads"
	StagePlan            = "plan"
	StageWriteOps        = "write-ops"
	StageTakeDown        = "take-down"
	StageAnswer          = "answer"
	StageOpen            = "open"
)

// Params is what one run reconciles threads with.
type Params struct {
	Reports   []string
	OpsPath   string
	EventPath string
	Apply     bool
	Reader    threadsstages.FindingsReader
}

// Inputs are p as the flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputReports:   p.Reports,
		InputReader:    p.Reader,
		InputOpsPath:   p.OpsPath,
		InputEventPath: p.EventPath,
		InputApply:     p.Apply,
	}
}

// New builds the flow.
//
// Trust and the repository come first, before anything else runs, as the
// clearance flow also declares them: the run reads the standing threads live
// even when it only writes the plan, with apply left unset, because the plan
// is a delta against them. The failure precedence — no repository, then no
// credential, then no event — falls out of declaring init-trust, init-scm and
// load-pull-request, in that order, before the stages that read and
// reconcile findings. Applying the plan is the run's gated tail: take-down,
// answer and open all run only when the caller asked to apply it, after the
// plan is computed and written down either way.
func New() (*flow.Flow, error) {
	apply := flow.FromInput(InputApply)
	repository := flow.FromStage(StageInitSCM, "Repository")
	ref := flow.FromStage(StageLoadPullRequest, "Ref")
	ops := flow.FromStage(StagePlan, "Ops")

	return flow.New(Name).
		Stage(StageInitTrust, truststages.InitTrust).
		Stage(StageInitSCM, scmstages.InitSCM).
		With("Trust", flow.FromStage(StageInitTrust, "Trusted")).
		Stage(StageLoadPullRequest, scmstages.LoadPullRequest).
		With("EventPath", flow.FromInput(InputEventPath)).
		Stage(StageReadFindings, threadsstages.ReadFindings).
		With("Reports", flow.FromInput(InputReports)).
		With("Reader", flow.FromInput(InputReader)).
		Stage(StageListThreads, threadsstages.ListThreads).
		With("Repository", repository).
		With("Ref", ref).
		Stage(StagePlan, threadsstages.Plan).
		With("Located", flow.FromStage(StageReadFindings, "Located")).
		With("Threads", flow.FromStage(StageListThreads, "Threads")).
		With("Ref", ref).
		Stage(StageWriteOps, threadsstages.WriteOps).
		With("Path", flow.FromInput(InputOpsPath)).
		With("Ops", ops).
		Stage(StageTakeDown, threadsstages.TakeDown).
		When(apply).
		With("Repository", repository).
		With("Ref", ref).
		With("Ops", ops).
		Stage(StageAnswer, threadsstages.Answer).
		When(apply).
		With("Repository", repository).
		With("Ref", ref).
		With("Ops", ops).
		Stage(StageOpen, threadsstages.Open).
		When(apply).
		With("Repository", repository).
		With("Ref", ref).
		With("Ops", ops).
		Build()
}
