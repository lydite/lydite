package main

import (
	"context"
	"fmt"
	"os"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// publishTarget is where a verdict is published: which repository, which
// revision, and which conversation.
type publishTarget struct {
	Client *forge.Client
	Repo   forge.Repo
	SHA    string
	Number int
}

// resolvePublishTarget reads the platform's environment.
//
// Every missing piece is an error rather than a quiet skip. A publishing
// step that silently does nothing is indistinguishable from one that worked,
// and the failure it hides — no token, no permission, the wrong event — is
// exactly the failure that leaves a pull request with no verdict on it
// while the job reports success.
func resolvePublishTarget(eventPath string) (publishTarget, error) {
	token := firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	if token == "" {
		return publishTarget{}, fmt.Errorf("--publish needs GITHUB_TOKEN (the workflow's `env:` block, with `statuses: write`)")
	}
	slug := os.Getenv("GITHUB_REPOSITORY")
	if slug == "" {
		return publishTarget{}, fmt.Errorf("--publish needs GITHUB_REPOSITORY, which the platform sets for every job")
	}
	repo, err := forge.ParseRepo(slug)
	if err != nil {
		return publishTarget{}, fmt.Errorf("GITHUB_REPOSITORY: %w", err)
	}
	if eventPath == "" {
		eventPath = os.Getenv("GITHUB_EVENT_PATH")
	}
	if eventPath == "" {
		return publishTarget{}, fmt.Errorf("--publish needs GITHUB_EVENT_PATH: the head revision is read from the event, not from the checkout")
	}
	event, err := forge.LoadPullRequestEvent(eventPath)
	if err != nil {
		return publishTarget{}, err
	}
	if event.PullRequest.Head.SHA == "" || event.Number == 0 {
		return publishTarget{}, fmt.Errorf("the event at %s names no pull request: --publish belongs on a pull_request trigger", eventPath)
	}
	return publishTarget{
		Client: forge.New(token),
		Repo:   repo,
		SHA:    event.PullRequest.Head.SHA,
		Number: event.Number,
	}, nil
}

// stateFor maps a run's verdict onto a commit status state.
//
// The three are distinct on purpose. A referral is `pending` because it is
// pending: a person has not answered yet. It blocks a required check exactly
// as hard as a failure does, so nothing is softened by saying so accurately
// — and calling a referral a failure is the one word CONTEXT.md rules out,
// because a gate fails and a referral does not.
func stateFor(verdict ui.Verdict) clearance.State {
	switch verdict {
	case ui.VerdictFail:
		return clearance.StateFailure
	case ui.VerdictRefer:
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
func describe(d referral.Decision, verdict ui.Verdict) string {
	switch {
	case verdict == ui.VerdictFail:
		return "exemption change not isolated — split it into its own pull request"
	case verdict == ui.VerdictRefer && d.Exemption != "":
		return fmt.Sprintf("%s matched, then disqualified — comment /lydite clear", d.Exemption)
	case verdict == ui.VerdictRefer:
		return "referred — comment /lydite clear"
	case d.Empty:
		return "no changes against the base"
	default:
		return "exempt: " + d.Exemption
	}
}

// publish records the verdict as the `lydite/referral` commit status.
//
// The status and nothing else. It is the whole record a clearance acts on, and
// it has to land early — a person can start clearing a referral while the test
// matrix is still running, which is the property ADR 0015 rests on and the
// reason this stays a separate step from the standing comment.
//
// The verdict also reaches the comment, as one section of it, but by the route
// every other command's results take: the report document this run wrote.
// Rendering it here as well would be a second derivation of one answer, and
// the two would drift.
func publish(ctx context.Context, target publishTarget, d referral.Decision, verdict ui.Verdict) error {
	return target.Client.PublishStatus(ctx, target.Repo, target.SHA,
		stateFor(verdict), describe(d, verdict), runURL())
}

// runURL points the status at the job that produced it, so a reader can
// reach the reasoning behind a one-line description.
func runURL() string {
	server, repo, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%s", server, repo, id)
}
