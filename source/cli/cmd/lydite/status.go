package main

import (
	"errors"
	"fmt"
	"os"

	"lydite/lydite/internal/forge"
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
		return publishTarget{}, noTokenError(what)
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

// noTokenError is a write refused for want of a credential, naming what
// needed one and where a workflow grants it.
func noTokenError(what string) error {
	return fmt.Errorf("%s needs GITHUB_TOKEN (the workflow's `env:` block, with `statuses: write`)", what)
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
//
// A thin wrapper over forge.LoadPullRequestRef, mapping its typed errors back
// onto this command's own wording.
func resolvePullRequest(what, eventPath string) (pullRequestRef, error) {
	if eventPath == "" {
		eventPath = os.Getenv("GITHUB_EVENT_PATH")
	}
	ref, err := forge.LoadPullRequestRef(eventPath)
	if err != nil {
		return pullRequestRef{}, pullRequestError(what, err)
	}
	return pullRequestRef{SHA: ref.SHA, Number: ref.Number}, nil
}

// pullRequestError is a failure to load the pull request an event points at,
// in the wording of what names itself in `what`: no event, and an event from
// some other trigger, each name the flag or command that needed one. Any
// other failure is returned as it is.
func pullRequestError(what string, err error) error {
	if errors.Is(err, forge.ErrNoEvent) {
		return fmt.Errorf("%s needs GITHUB_EVENT_PATH: the head revision is read from the event, not from the checkout", what)
	}
	var notAPullRequest *forge.NotAPullRequestError
	if errors.As(err, &notAPullRequest) {
		return fmt.Errorf("the event at %s names no pull request: %s belongs on a pull_request trigger", notAPullRequest.Path, what)
	}
	return err
}

// statusOutFlag names the flag that renders the status instead of posting it.
const statusOutFlag = "--status-out"

// publishFlag names the flag that publishes the status, and on its own posts
// it here.
const publishFlag = "--publish"

// runURL points the status at the job that produced it, so a reader can
// reach the reasoning behind a one-line description.
func runURL() string {
	server, repo, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%s", server, repo, id)
}
