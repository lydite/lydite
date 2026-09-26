// Package clearanceflow declares the flow that answers a lydite command posted
// on a pull request: establishing the run's trust and the repository it acts
// on, resolving the comment live, deciding what the command does, and
// recording, rendering or replying with the answer.
//
// It is a declaration and nothing else. Every stage lives in its own package
// and every value the flow starts from is an input its caller supplies, so
// nothing here reads the environment, prints, or chooses a report row: what
// the run produced is read back off the Result by the stage names below.
package clearanceflow

import (
	"io"

	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/reviewdecision"
	clearancestages "lydite/lydite/internal/stages/clearance"
	scmstages "lydite/lydite/internal/stages/scm"
	truststages "lydite/lydite/internal/stages/trust"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "clearance"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputCommentRef is the forge.CommentRef the event payload points at.
	InputCommentRef = "CommentRef"
	// InputDir is the scan root: where the exemptions in force are read, and
	// the checkout a clearance's decision is recomputed over.
	InputDir = "Dir"
	// InputBase and InputBaseBranch resolve the commit a cleared decision is
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
	// InputVersion is lydite's own version, for a reply's footer.
	InputVersion = "Version"
	// InputTargetURL points a recorded status at the job that produced it.
	InputTargetURL = "TargetURL"
	// InputStatusOut is where a clearance is rendered instead of posted.
	InputStatusOut = "StatusOut"
	// InputRender is the bool choosing rendering over posting.
	InputRender = "Render"
)

// The names of the flow's stages, in the order they run.
const (
	StageInitTrust         = "init-trust"
	StageInitSCM           = "init-scm"
	StageLoadComment       = "load-comment"
	StageParseCommand      = "parse-command"
	StageResolveHead       = "resolve-head"
	StageCheckPermission   = "check-permission"
	StageReadReferral      = "read-referral"
	StageDecide            = "decide"
	StageFingerprint       = "fingerprint"
	StageDescribeClearance = "describe-clearance"
	StageRecordStatuses    = "record-statuses"
	StageRenderStatuses    = "render-statuses"
	StageReplyClearance    = "reply-clearance"
	StageComposeReply      = "compose-reply"
	StageReply             = "reply"
)

// Params is what one run answers a comment with.
type Params struct {
	CommentRef       forge.CommentRef
	Dir              string
	Base             string
	BaseBranch       string
	SurfacesDocument string
	Toolchains       reviewdecision.Toolchains
	Progress         io.Writer
	Version          string
	TargetURL        string
	// StatusOut names where a clearance is rendered instead of posted, and is
	// empty for a run that posts it.
	StatusOut string
}

// Inputs are p as the flow is run with them.
//
// Whether a clearance is rendered or posted is derived here from StatusOut
// alone, so the two can never disagree: a run told to render at no path, or
// to post while also naming one, is not a run anybody can ask for.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputCommentRef:       p.CommentRef,
		InputDir:              p.Dir,
		InputBase:             p.Base,
		InputBaseBranch:       p.BaseBranch,
		InputSurfacesDocument: p.SurfacesDocument,
		InputToolchains:       p.Toolchains,
		InputProgress:         p.Progress,
		InputVersion:          p.Version,
		InputTargetURL:        p.TargetURL,
		InputStatusOut:        p.StatusOut,
		InputRender:           p.StatusOut != "",
	}
}

// New builds the flow.
//
// Trust and the repository come first, before the comment is even read: the
// comment is resolved live from its id, which needs both, and a payload
// naming a repository other than the trusted one is refused before anything
// is fetched. Every stage past parse-command runs only when the comment is a
// command addressed to lydite on a pull request, and that condition is
// declared first on each, so a comment nothing addressed never reads an
// output of a stage that did not run.
//
// A clearance is recorded or rendered — one of the two, never both — before
// its reply is posted, and a failure to record it fails the run with no reply
// saying it was cleared. A reply that cannot be posted is recorded on the
// Result rather than failing the run: the decision already stands, and the
// reply stages are the only ones whose errors Result.Errors holds.
func New() (*flow.Flow, error) {
	addressed := flow.FromStage(StageParseCommand, "Addressed")
	clears := flow.FromStage(StageDecide, "Clears")
	answers := flow.FromStage(StageDecide, "Answers")
	repository := flow.FromStage(StageInitSCM, "Repository")
	number := flow.FromStage(StageParseCommand, "Number")
	login := flow.FromStage(StageParseCommand, "Login")
	head := flow.FromStage(StageResolveHead, "SHA")
	status := flow.FromStage(StageReadReferral, "Status")
	description := flow.FromStage(StageDescribeClearance, "Description")

	return flow.New(Name).
		Stage(StageInitTrust, truststages.InitTrust).
		Stage(StageInitSCM, scmstages.InitSCM).
		With("Trust", flow.FromStage(StageInitTrust, "Trusted")).
		Stage(StageLoadComment, scmstages.LoadComment).
		With("Trust", flow.FromStage(StageInitTrust, "Trusted")).
		With("Repository", repository).
		With("Ref", flow.FromInput(InputCommentRef)).
		Stage(StageParseCommand, clearancestages.ParseCommand).
		With("Comment", flow.FromStage(StageLoadComment, "Comment")).
		Stage(StageResolveHead, scmstages.ResolveHead).
		When(addressed).
		With("Repository", repository).
		With("Number", number).
		Stage(StageCheckPermission, scmstages.CheckPermission).
		When(addressed).
		With("Repository", repository).
		With("Login", login).
		Stage(StageReadReferral, scmstages.ReadStatus).
		When(addressed).
		With("Repository", repository).
		With("SHA", head).
		Stage(StageDecide, clearancestages.Decide).
		When(addressed).
		With("Repository", repository).
		With("Dir", flow.FromInput(InputDir)).
		With("Number", number).
		With("Command", flow.FromStage(StageParseCommand, "Command")).
		With("Head", head).
		With("CanWrite", flow.FromStage(StageCheckPermission, "CanWrite")).
		With("Status", status).
		With("CommentAt", flow.FromStage(StageParseCommand, "CommentAt")).
		Stage(StageFingerprint, clearancestages.Fingerprint).
		When(addressed).When(clears).
		With("Repository", repository).
		With("Dir", flow.FromInput(InputDir)).
		With("Base", flow.FromInput(InputBase)).
		With("BaseBranch", flow.FromInput(InputBaseBranch)).
		With("SurfacesDocument", flow.FromInput(InputSurfacesDocument)).
		With("Number", number).
		With("Head", head).
		With("Toolchains", flow.FromInput(InputToolchains)).
		With("Progress", flow.FromInput(InputProgress)).
		Stage(StageDescribeClearance, clearancestages.DescribeClearance).
		When(addressed).When(clears).
		With("Login", login).
		With("Head", head).
		With("Fingerprint", flow.FromStage(StageFingerprint, "Fingerprint")).
		With("Version", flow.FromInput(InputVersion)).
		Stage(StageRecordStatuses, clearancestages.RecordStatuses).
		When(addressed).When(clears).Unless(flow.FromInput(InputRender)).
		With("Repository", repository).
		With("Head", head).
		With("Number", number).
		With("Description", description).
		With("TargetURL", flow.FromInput(InputTargetURL)).
		Stage(StageRenderStatuses, clearancestages.RenderStatuses).
		When(addressed).When(clears).When(flow.FromInput(InputRender)).
		With("Path", flow.FromInput(InputStatusOut)).
		With("Head", head).
		With("Number", number).
		With("Description", description).
		With("TargetURL", flow.FromInput(InputTargetURL)).
		Stage(StageReplyClearance, scmstages.PostComment).
		When(addressed).When(clears).
		With("Repository", repository).
		With("Number", number).
		With("Body", flow.FromStage(StageDescribeClearance, "Body")).
		OnError(flow.RecordAndContinue).
		Stage(StageComposeReply, clearancestages.ComposeReply).
		When(addressed).When(answers).
		With("Action", flow.FromStage(StageDecide, "Action")).
		With("Login", login).
		With("Head", head).
		With("Status", status).
		With("Version", flow.FromInput(InputVersion)).
		Stage(StageReply, scmstages.PostComment).
		When(addressed).When(answers).
		With("Repository", repository).
		With("Number", number).
		With("Body", flow.FromStage(StageComposeReply, "Body")).
		OnError(flow.RecordAndContinue).
		Build()
}
