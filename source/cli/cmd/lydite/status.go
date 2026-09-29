package main

import (
	"errors"
	"fmt"
	"os"

	"lydite/lydite/internal/forge"
)

// noTokenError is a write refused for want of a credential, naming what
// needed one and where a workflow grants it.
func noTokenError(what string) error {
	return fmt.Errorf("%s needs GITHUB_TOKEN (the workflow's `env:` block, with `statuses: write`)", what)
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
