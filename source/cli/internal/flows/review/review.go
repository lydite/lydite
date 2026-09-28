// Package reviewflow declares the two flows that answer `lydite review` and
// `lydite review compare`: comparing a change's opted-in components against
// their merge-base, deciding what the comparison means, and — for review
// alone — publishing the verdict as a commit status, directly or rendered for
// another step to post.
//
// It is a declaration and nothing else. Every stage lives in its own package
// and every value a flow starts from is an input its caller supplies, so
// nothing here reads the environment, prints, or chooses a report row: what
// the run produced is read back off the Result by the stage names below.
package reviewflow

import (
	"io"

	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/reviewdecision"
	reviewstages "lydite/lydite/internal/stages/review"
	scmstages "lydite/lydite/internal/stages/scm"
	truststages "lydite/lydite/internal/stages/trust"
)

// Name is the review flow's name, which every error it raises for a stage
// carries.
const Name = "review"

// The keys of the flow.Inputs the review flow is run with. Params.Inputs
// supplies every one of them.
const (
	// InputDir is the scan root: where the component declaration and the
	// exemptions in force are read.
	InputDir = "Dir"
	// InputBase and InputBaseBranch resolve the commit the change is
	// measured against.
	InputBase       = "Base"
	InputBaseBranch = "BaseBranch"
	// InputSurfacesDocument names a comparison `review compare` already made,
	// and is empty for a run that makes it itself.
	InputSurfacesDocument = "SurfacesDocument"
	// InputToolchains is the reviewdecision.Toolchains a comparison made in
	// this process provisions through.
	InputToolchains = "Toolchains"
	// InputProgress is the io.Writer a comparison's own tool reports to.
	InputProgress = "Progress"
	// InputEventPath is the pull request's event payload, already resolved by
	// the caller. Empty is no pull request, which a local review has none of.
	InputEventPath = "EventPath"
	// InputReports are the report directories whose scan documents supply the
	// licence and SCA evidence a conditional exemption needs.
	InputReports = "Reports"
	// InputScan reads a report directory's scan document.
	InputScan = "Scan"
	// InputTargetURL points a posted or rendered status at the job that
	// produced it.
	InputTargetURL = "TargetURL"
	// InputStatusOut is where a status is rendered instead of posted.
	InputStatusOut = "StatusOut"
	// InputPublish chooses whether the run publishes a status at all — posted
	// or rendered — and is also the guard against running a comparison's own
	// build code in a process that holds, or is about to use, a publishing
	// credential.
	InputPublish = "Publish"
	// InputPostsDirectly chooses posting the status here, under this
	// process's own credential, over rendering it for another step.
	InputPostsDirectly = "PostsDirectly"
	// InputRendersOnly chooses rendering the status for another step to post,
	// over posting it here.
	InputRendersOnly = "RendersOnly"
)

// The names of the review flow's stages, in the order they run.
const (
	StageResolveBase     = "resolve-base"
	StageSurfaces        = "surfaces"
	StageDecide          = "decide"
	StageCheckDirty      = "check-dirty"
	StageInitTrust       = "init-trust"
	StageInitSCM         = "init-scm"
	StageLoadPullRequest = "load-pull-request"
	StageComposeStatus   = "compose-status"
	StageRenderStatus    = "render-status"
	StagePostStatus      = "post-status"
)

// Params is what one run reviews with.
type Params struct {
	Dir              string
	Base             string
	BaseBranch       string
	SurfacesDocument string
	Toolchains       reviewdecision.Toolchains
	Progress         io.Writer
	EventPath        string
	Reports          []string
	Scan             reviewdecision.ScanReader
	TargetURL        string
	// StatusOut names where a status is rendered instead of posted, and is
	// empty for a run that posts it or does not publish at all.
	StatusOut string
	// Publish chooses whether the run publishes a status at all.
	Publish bool
	// PostsDirectly chooses posting the status here over rendering it, and
	// only matters when Publish is set.
	PostsDirectly bool
	// RendersOnly chooses rendering the status for another step to post, over
	// posting it here, and only matters when Publish is set.
	RendersOnly bool
}

// Inputs are p as the review flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDir:              p.Dir,
		InputBase:             p.Base,
		InputBaseBranch:       p.BaseBranch,
		InputSurfacesDocument: p.SurfacesDocument,
		InputToolchains:       p.Toolchains,
		InputProgress:         p.Progress,
		InputEventPath:        p.EventPath,
		InputReports:          p.Reports,
		InputScan:             p.Scan,
		InputTargetURL:        p.TargetURL,
		InputStatusOut:        p.StatusOut,
		InputPublish:          p.Publish,
		InputPostsDirectly:    p.PostsDirectly,
		InputRendersOnly:      p.RendersOnly,
	}
}

// New builds the review flow.
//
// Publishing is the run's gated tail: every stage through check-dirty runs
// unconditionally, computing the decision a plain run reports and a
// publishing run also posts or renders. The credential stages are declared
// late, after the decision and the dirty check, and conditioned on
// posts-directly rather than on publish alone — a render-only run needs no
// credential at all — so the decision is computed and its warnings are
// available to the caller before any credential is asked for.
func New() (*flow.Flow, error) {
	publish := flow.FromInput(InputPublish)
	postsDirectly := flow.FromInput(InputPostsDirectly)
	rendersOnly := flow.FromInput(InputRendersOnly)
	base := flow.FromStage(StageResolveBase, "Base")

	return flow.New(Name).
		Stage(StageResolveBase, reviewstages.ResolveBase).
		With("Dir", flow.FromInput(InputDir)).
		With("Base", flow.FromInput(InputBase)).
		With("BaseBranch", flow.FromInput(InputBaseBranch)).
		Stage(StageSurfaces, reviewstages.Surfaces).
		With("Dir", flow.FromInput(InputDir)).
		With("Base", base).
		With("SurfacesDocument", flow.FromInput(InputSurfacesDocument)).
		With("GuardCredential", publish).
		With("Toolchains", flow.FromInput(InputToolchains)).
		With("Progress", flow.FromInput(InputProgress)).
		Stage(StageDecide, reviewstages.Decide).
		With("Dir", flow.FromInput(InputDir)).
		With("Base", base).
		With("Surfaces", flow.FromStage(StageSurfaces, "Surfaces")).
		With("EventPath", flow.FromInput(InputEventPath)).
		With("Reports", flow.FromInput(InputReports)).
		With("Scan", flow.FromInput(InputScan)).
		Stage(StageCheckDirty, reviewstages.CheckDirty).
		With("Dir", flow.FromInput(InputDir)).
		Stage(StageInitTrust, truststages.InitTrust).
		When(publish).When(postsDirectly).
		Stage(StageInitSCM, scmstages.InitSCM).
		When(publish).When(postsDirectly).
		With("Trust", flow.FromStage(StageInitTrust, "Trusted")).
		Stage(StageLoadPullRequest, scmstages.LoadPullRequest).
		When(publish).
		With("EventPath", flow.FromInput(InputEventPath)).
		Stage(StageComposeStatus, reviewstages.ComposeStatus).
		When(publish).
		With("Result", flow.FromStage(StageDecide, "Result")).
		With("Ref", flow.FromStage(StageLoadPullRequest, "Ref")).
		With("TargetURL", flow.FromInput(InputTargetURL)).
		Stage(StageRenderStatus, reviewstages.RenderStatus).
		When(publish).When(rendersOnly).
		With("Path", flow.FromInput(InputStatusOut)).
		With("Status", flow.FromStage(StageComposeStatus, "Status")).
		Stage(StagePostStatus, scmstages.PostStatus).
		When(publish).When(postsDirectly).
		With("Repository", flow.FromStage(StageInitSCM, "Repository")).
		With("Status", flow.FromStage(StageComposeStatus, "Status")).
		Build()
}

// CompareName is the review-compare flow's name.
const CompareName = "review-compare"

// The keys of the flow.Inputs the review-compare flow is run with.
// CompareParams.Inputs supplies every one of them.
const (
	// InputCompareDir is the scan root: where the component declaration is
	// read.
	InputCompareDir = "Dir"
	// InputCompareBase and InputCompareBaseBranch resolve the commit the
	// change is measured against.
	InputCompareBase       = "Base"
	InputCompareBaseBranch = "BaseBranch"
	// InputCompareToolchains is the reviewdecision.Toolchains the comparison
	// provisions through.
	InputCompareToolchains = "Toolchains"
	// InputCompareProgress is the io.Writer the comparison's own tool reports
	// to.
	InputCompareProgress = "Progress"
	// InputComparePath is where the comparison document is written.
	InputComparePath = "Path"
)

// The names of the review-compare flow's stages, in the order they run.
const (
	StageCompareResolveBase = "resolve-base"
	StageCompareSurfaces    = "compare-surfaces"
	StageWriteSurfaces      = "write-surfaces"
)

// CompareParams is what one run compares with.
type CompareParams struct {
	Dir        string
	Base       string
	BaseBranch string
	Toolchains reviewdecision.Toolchains
	Progress   io.Writer
	// Path is where the comparison document is written.
	Path string
}

// Inputs are p as the review-compare flow is run with them.
func (p CompareParams) Inputs() flow.Inputs {
	return flow.Inputs{
		InputCompareDir:        p.Dir,
		InputCompareBase:       p.Base,
		InputCompareBaseBranch: p.BaseBranch,
		InputCompareToolchains: p.Toolchains,
		InputCompareProgress:   p.Progress,
		InputComparePath:       p.Path,
	}
}

// NewCompare builds the review-compare flow: resolve the base, compare every
// opted-in component against it, and write the comparison as a document for
// `review` to read later.
//
// The guard a shared comparison runs under is bound to false here, not to an
// input: this flow's whole purpose is making the comparison, in a job that by
// definition holds no publishing credential yet to guard against — see
// internal/stages/review's CompareSurfaces.
func NewCompare() (*flow.Flow, error) {
	return flow.New(CompareName).
		Stage(StageCompareResolveBase, reviewstages.ResolveBase).
		With("Dir", flow.FromInput(InputCompareDir)).
		With("Base", flow.FromInput(InputCompareBase)).
		With("BaseBranch", flow.FromInput(InputCompareBaseBranch)).
		Stage(StageCompareSurfaces, reviewstages.CompareSurfaces).
		With("Dir", flow.FromInput(InputCompareDir)).
		With("Base", flow.FromStage(StageCompareResolveBase, "Base")).
		With("GuardCredential", flow.Literal(false)).
		With("Toolchains", flow.FromInput(InputCompareToolchains)).
		With("Progress", flow.FromInput(InputCompareProgress)).
		Stage(StageWriteSurfaces, reviewstages.WriteSurfaces).
		With("Path", flow.FromInput(InputComparePath)).
		With("Base", flow.FromStage(StageCompareResolveBase, "Base")).
		With("Surfaces", flow.FromStage(StageCompareSurfaces, "Surfaces")).
		Build()
}
