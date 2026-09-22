package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// pullRequestRef is the revision and the conversation a verdict is about.
type pullRequestRef struct {
	SHA    string
	Number int
}

// resolveTarget reads the platform's environment on behalf of what names
// itself in `what` — a flag, or a command.
//
// Every missing piece is an error rather than a quiet skip. A step that
// silently does nothing is indistinguishable from one that worked, and the
// failure it hides — no token, no permission, the wrong event — is exactly the
// failure that leaves a pull request with no verdict on it while the job
// reports success.
func resolveTarget(what, eventPath string) (publishTarget, error) {
	token := firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	if token == "" {
		return publishTarget{}, fmt.Errorf("%s needs GITHUB_TOKEN (the workflow's `env:` block, with `statuses: write`)", what)
	}
	slug := os.Getenv("GITHUB_REPOSITORY")
	if slug == "" {
		return publishTarget{}, fmt.Errorf("%s needs GITHUB_REPOSITORY, which the platform sets for every job", what)
	}
	repo, err := forge.ParseRepo(slug)
	if err != nil {
		return publishTarget{}, fmt.Errorf("GITHUB_REPOSITORY: %w", err)
	}
	ref, err := resolvePullRequest(what, eventPath)
	if err != nil {
		return publishTarget{}, err
	}
	return publishTarget{
		Client: forge.New(token),
		Repo:   repo,
		SHA:    ref.SHA,
		Number: ref.Number,
	}, nil
}

// resolvePullRequest reads the revision and the conversation out of the
// platform's event payload.
//
// One implementation for every command that writes to a pull request, so a
// verdict and the threads under it can never be about two different
// revisions: the head is read from the event payload and not from the
// checkout, which on a pull_request event is a merge commit that exists on no
// branch.
//
// It asks for no credential, because a run that renders a status document for
// another step to post holds none — the whole point of computing a verdict in
// a job with no token. What names the pull request is the event either way.
func resolvePullRequest(what, eventPath string) (pullRequestRef, error) {
	if eventPath == "" {
		eventPath = os.Getenv("GITHUB_EVENT_PATH")
	}
	if eventPath == "" {
		return pullRequestRef{}, fmt.Errorf("%s needs GITHUB_EVENT_PATH: the head revision is read from the event, not from the checkout", what)
	}
	event, err := forge.LoadPullRequestEvent(eventPath)
	if err != nil {
		return pullRequestRef{}, err
	}
	if event.PullRequest.Head.SHA == "" || event.Number == 0 {
		return pullRequestRef{}, fmt.Errorf("the event at %s names no pull request: %s belongs on a pull_request trigger", eventPath, what)
	}
	return pullRequestRef{SHA: event.PullRequest.Head.SHA, Number: event.Number}, nil
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

// referralStatus is the verdict as the document both routes carry, so the
// status a step posts and the one this process would have posted itself are
// one derivation rather than two.
func referralStatus(ref pullRequestRef, d referral.Decision, verdict ui.Verdict) forge.Status {
	return forge.Status{
		State:       stateFor(verdict),
		Context:     clearance.Context,
		Description: describe(d, verdict),
		TargetURL:   runURL(),
		SHA:         ref.SHA,
		PullRequest: ref.Number,
	}
}

// publish records the verdict as the `lydite/referral` commit status: posted
// here with the job's own token, or rendered at out for a step that posts it
// under lydite's App identity.
//
// The two are alternatives the caller chooses between, not a ladder. A
// repository that has not adopted the reusable workflows posts directly and
// keeps every property of the status; neither route is attempted after the
// other, because a verdict published twice under two identities is the mixed
// record the App identity exists to end.
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
func publish(ctx context.Context, out, eventPath string, d referral.Decision, verdict ui.Verdict) error {
	if out != "" {
		ref, err := resolvePullRequest(statusOutFlag, eventPath)
		if err != nil {
			return err
		}
		return forge.WriteStatus(out, referralStatus(ref, d, verdict))
	}
	target, err := resolveTarget("--publish", eventPath)
	if err != nil {
		return err
	}
	return target.Client.PostStatus(ctx, target.Repo,
		referralStatus(pullRequestRef{SHA: target.SHA, Number: target.Number}, d, verdict))
}

// clearanceStatus is a clearance as the document both routes carry, so the
// status a step posts and the one this process would have posted itself are
// one derivation rather than two.
//
// It names the pull request for the same reason the referral document does,
// and for one more: a clearance run is an issue_comment run, whose own claims
// name a branch rather than a pull ref, so the conversation is in the document
// or nowhere.
func clearanceStatus(ref pullRequestRef, description string) forge.Status {
	return forge.Status{
		State:       clearance.StateSuccess,
		Context:     clearance.ClearanceContext,
		Description: description,
		TargetURL:   runURL(),
		SHA:         ref.SHA,
		PullRequest: ref.Number,
	}
}

// recordClearance records the clearance: posted here with the job's own
// token, or rendered at out for a step that posts it under lydite's App
// identity.
//
// The two are alternatives the caller chooses between, not a ladder, for the
// same reason publish's are, and they are not the same write.
//
// Either route records two statuses on the head: `lydite/clearance`, which
// says who cleared the revision, and `lydite/referral` resolved to success,
// which is what a required check is gated on. A clearance that records only
// the first leaves every consumer's pull request blocked on a referral nobody
// can resolve.
//
// The rendered route writes them as two documents — the one --status-out
// names, and its referralDocument sibling — rather than as one document
// carrying both. Each is a single status object, which is what the relay's
// /status route and the posting step's `jq -r .context` each read, and the
// relay admits a clearance ref to `lydite/clearance` alone: the referral
// document is the posting step's to write with the job's own token, and is
// never relayed.
//
// The clearance goes first on both routes: if the second write or post fails,
// the run fails loudly and the pull request holds a clearance record beside a
// referral still standing, which a repeated comment repairs. The other order
// could leave a green referral with nothing recording who cleared it.
//
// A document that cannot be written, or a post that fails, fails the run — a
// clearance nothing recorded leaves the referral standing while the job that
// answered the comment reports success.
func recordClearance(ctx context.Context, client *forge.Client, repo forge.Repo, out string, s forge.Status) error {
	resolved := s
	resolved.Context = clearance.Context
	if out != "" {
		if err := forge.WriteStatus(out, s); err != nil {
			return err
		}
		return forge.WriteStatus(referralDocument(out), resolved)
	}
	if err := client.PostStatus(ctx, repo, s); err != nil {
		return err
	}
	return client.PostStatus(ctx, repo, resolved)
}

// referralDocument names the second document a rendered clearance writes,
// beside the one --status-out named.
//
// The path is derived rather than configured so that the step posting the
// documents computes it from the path it already passed, and a lydite that
// can render a clearance can always render the referral resolving it. A flag
// for the second path would make resolving the referral something a caller
// could omit, which is the same pull request blocked on a pending gate.
func referralDocument(out string) string {
	ext := filepath.Ext(out)
	return strings.TrimSuffix(out, ext) + ".referral" + ext
}

// statusOutFlag names the flag that renders the status instead of posting it.
const statusOutFlag = "--status-out"

// runURL points the status at the job that produced it, so a reader can
// reach the reasoning behind a one-line description.
func runURL() string {
	server, repo, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%s", server, repo, id)
}
