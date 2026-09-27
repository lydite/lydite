package reviewstages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// ComposeStatusIn is what the review's commit status is composed from.
type ComposeStatusIn struct {
	// Result is the decision Decide reached; its Verdict and Decision are
	// what the status states.
	Result reviewdecision.Result
	// Ref is the pull request the status is about. Its SHA is the revision the
	// run measured, never a live read of the head: a push landing mid-run
	// would otherwise put this verdict on a commit nothing measured.
	Ref forge.PullRequestRef
	// TargetURL points the status at the job that produced it, and is empty
	// where there is no such job.
	TargetURL string
}

// ComposeStatusOut is the status, as both publishing routes carry it.
type ComposeStatusOut struct {
	Status forge.Status
}

// ComposeStatus composes the review's verdict as the `lydite/referral` commit
// status, the one document both routes carry — posted directly, or rendered for
// another step to post — so the two are one derivation rather than two.
//
// A verdict this does not name is refused rather than published: every
// unnamed value would otherwise fall through to success, and a status saying a
// change may merge unattended is the one answer that has to have been decided.
func ComposeStatus(_ context.Context, in ComposeStatusIn) (ComposeStatusOut, error) {
	switch in.Result.Verdict {
	case reviewdecision.VerdictPass, reviewdecision.VerdictRefer, reviewdecision.VerdictFail:
	default:
		return ComposeStatusOut{}, fmt.Errorf("review: verdict %q has no commit status", in.Result.Verdict)
	}
	return ComposeStatusOut{Status: forge.Status{
		State:       stateFor(in.Result.Verdict),
		Context:     clearance.Context,
		Description: describe(in.Result.Decision, in.Result.Verdict),
		TargetURL:   in.TargetURL,
		SHA:         in.Ref.SHA,
		PullRequest: in.Ref.Number,
	}}, nil
}

// stateFor maps a review's verdict onto a commit status state.
//
// The three are distinct on purpose. A referral is `pending` because it is
// pending: a person has not answered yet. It blocks a required check exactly
// as hard as a failure does, so nothing is softened by saying so accurately
// — and calling a referral a failure is the one word CONTEXT.md rules out,
// because a gate fails and a referral does not.
func stateFor(verdict reviewdecision.Verdict) clearance.State {
	switch verdict {
	case reviewdecision.VerdictFail:
		return clearance.StateFailure
	case reviewdecision.VerdictRefer:
		return clearance.StatePending
	default:
		return clearance.StateSuccess
	}
}

// describe is the one line shown beside the status.
//
// A pending status renders as a yellow dot, which is what a job still
// running looks like. The description is the only thing that distinguishes
// them, so it names what is being waited for rather than restating the
// state.
func describe(d referral.Decision, verdict reviewdecision.Verdict) string {
	switch {
	case verdict == reviewdecision.VerdictFail:
		return "exemption change not isolated — split it into its own pull request"
	case verdict == reviewdecision.VerdictRefer && d.Exemption != "":
		return fmt.Sprintf("%s matched, then disqualified — comment /lydite clear", d.Exemption)
	case verdict == reviewdecision.VerdictRefer:
		return "referred — comment /lydite clear"
	case d.Empty:
		return "no changes against the base"
	default:
		return "exempt: " + d.Exemption
	}
}

// RenderStatusIn is the status RenderStatus writes, and where.
type RenderStatusIn struct {
	// Path is where the status document is written.
	Path   string
	Status forge.Status
}

// RenderStatus writes the status as a document for another step to post under
// lydite's App identity, instead of posting it here. The document is the whole
// of the write, so this needs no credential: the step that posts it holds the
// identity, and this run holds only the verdict.
func RenderStatus(_ context.Context, in RenderStatusIn) (struct{}, error) {
	return struct{}{}, forge.WriteStatus(in.Path, in.Status)
}
