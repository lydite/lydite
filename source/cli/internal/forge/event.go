package forge

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// CommentEvent is the part of an issue_comment payload a decision is made
// from.
//
// The payload is read whole rather than assembled from flags a driver chose
// to forward. A workflow writes exactly this document to the path in
// GITHUB_EVENT_PATH, and it is the same document the platform delivers to an
// app, so the deciding side does not learn which one invoked it.
type CommentEvent struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
		// PullRequest is present only on a comment on a pull request.
		// An issue comment names no revision and there is nothing to
		// decide about it.
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// LoadCommentEvent reads a webhook payload from disk.
func LoadCommentEvent(path string) (CommentEvent, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the platform's own GITHUB_EVENT_PATH or the --event flag, supplied by whoever runs lydite, not untrusted remote input
	if err != nil {
		return CommentEvent{}, fmt.Errorf("reading the event payload: %w", err)
	}
	var event CommentEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return CommentEvent{}, fmt.Errorf("parsing the event payload: %w", err)
	}
	return event, nil
}

// OnPullRequest reports whether the comment is on a pull request.
func (e CommentEvent) OnPullRequest() bool { return e.Issue.PullRequest != nil }

// PullRequestEvent is the part of a pull_request payload the producing side
// needs: which pull request, and which revision is its head.
//
// The head is taken from the payload rather than from GITHUB_SHA, and the
// difference is not cosmetic. On a pull_request event the checked-out
// revision is a merge commit the platform synthesises, which exists on no
// branch and which no clearance can ever be given for. Publishing a verdict
// against it would put the status somewhere nobody looks.
type PullRequestEvent struct {
	Number      int `json:"number"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

// LoadPullRequestEvent reads a pull_request webhook payload from disk.
func LoadPullRequestEvent(path string) (PullRequestEvent, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the platform's own GITHUB_EVENT_PATH or the --event flag, supplied by whoever runs lydite, not untrusted remote input
	if err != nil {
		return PullRequestEvent{}, fmt.Errorf("reading the event payload: %w", err)
	}
	var event PullRequestEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return PullRequestEvent{}, fmt.Errorf("parsing the event payload: %w", err)
	}
	if event.Number == 0 {
		event.Number = event.PullRequest.Number
	}
	return event, nil
}
