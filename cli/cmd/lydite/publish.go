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

// buildComment renders the standing verdict for the pull request.
//
// The table's columns follow the design: what was checked, what the head
// says, what the base says. A referral has no measurements to compare, so
// the head column carries what the change contains and the base column what
// was read out of the merge-base — which is the only place the exemption set
// is ever read from, and the reason a change cannot exempt itself.
func buildComment(d referral.Decision, ch referral.Change, declared int, verdict ui.Verdict, base string) ui.Comment {
	comment := ui.Comment{
		Verdict:  verdict,
		Headline: describe(d, verdict),
		Version:  version,
		Base:     shortSHA(base),
		Rows: []ui.CommentRow{
			{Check: "Changed paths", Head: fmt.Sprintf("%d", len(ch.Paths))},
			{Check: "Disqualifiers", Head: fmt.Sprintf("%d", len(d.Disqualifications))},
			{Check: "Exemptions", Base: fmt.Sprintf("%d declared", declared)},
		},
	}
	if len(d.Bundled) > 0 {
		comment.Sections = append(comment.Sections, ui.CommentSection{
			Title: "Bundled with the exemption change",
			Items: code(capped(d.Bundled)),
		})
	}
	if vetoes := capped(kinds(d.Disqualifications)); len(vetoes) > 0 {
		comment.Sections = append(comment.Sections, ui.CommentSection{Title: "Disqualifiers", Items: vetoes})
	}
	if len(d.Uncovered) > 0 {
		comment.Sections = append(comment.Sections, ui.CommentSection{
			Title: "Covered by no exemption",
			Items: code(capped(d.Uncovered)),
		})
	}
	return comment
}

func kinds(ds []referral.Disqualification) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, fmt.Sprintf("**%s** — %s", d.Kind, d.Evidence))
	}
	return out
}

// code wraps a path so a name containing markdown renders as itself.
func code(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, "`"+item+"`")
	}
	return out
}

// publish records the verdict as a status and as the standing comment.
//
// The status is written first and its failure is returned: the status is the
// record a clearance acts on, and losing it means a referral nothing can
// resolve. The comment is an explanation of that record, so failing to
// update it is reported and does not fail the run.
func publish(ctx context.Context, target publishTarget, d referral.Decision, ch referral.Change, declared int, verdict ui.Verdict, base string) error {
	state := stateFor(verdict)
	if err := target.Client.PublishStatus(ctx, target.Repo, target.SHA, state, describe(d, verdict), runURL()); err != nil {
		return err
	}
	body := buildComment(d, ch, declared, verdict, base).Render()
	if err := target.Client.UpsertComment(ctx, target.Repo, target.Number, ui.Marker, body); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: the verdict was published but its comment was not: %v\n", err)
	}
	return nil
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
