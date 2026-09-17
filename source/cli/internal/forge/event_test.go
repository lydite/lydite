package forge

import (
	"os"
	"path/filepath"
	"testing"
)

// The title is what a squash merge lands, so a breaking change declared there
// is the one the history keeps — a payload read without it would report the
// declaration missing.
func TestLoadPullRequestEventReadsTheTitleAndTheHead(t *testing.T) {
	payload := `{
	  "number": 7,
	  "pull_request": {
	    "number": 7,
	    "title": "feat!: drop the v1 client",
	    "head": {"sha": "c0ffee"}
	  }
	}`
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	event, err := LoadPullRequestEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	if event.PullRequest.Title != "feat!: drop the v1 client" {
		t.Errorf("Title = %q, want the title the payload carries", event.PullRequest.Title)
	}
	if event.Number != 7 || event.PullRequest.Head.SHA != "c0ffee" {
		t.Errorf("event = %+v, want pull request 7 at c0ffee", event)
	}
}

// A payload carrying no title is a pull request with an empty one, not a load
// error: every other field a caller needs is still there, and a title nobody
// wrote declares nothing.
func TestLoadPullRequestEventWithoutATitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"pull_request":{"number":9,"head":{"sha":"abc"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	event, err := LoadPullRequestEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	if event.PullRequest.Title != "" || event.Number != 9 {
		t.Errorf("event = %+v, want number 9 taken from the pull request and no title", event)
	}
}
