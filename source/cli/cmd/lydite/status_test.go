package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/ui"
)

// headEvent writes the payload a pull_request run is given, naming the
// repository's own head — the revision a status is about.
func headEvent(t *testing.T, dir string, number int) (path, head string) {
	t.Helper()
	head = strings.TrimSpace(executil.RunQuiet(context.Background(), dir, "git", "rev-parse", "HEAD").Output)
	raw, err := json.Marshal(map[string]any{
		"number":       number,
		"pull_request": map[string]any{"head": map[string]any{"sha": head}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, head
}

// The rendered document is the whole write, so it carries every field the step
// that posts it needs — the pull request included, which is what places the
// verdict for a poster that resolves the head itself.
//
// It is rendered with no credential in the environment at all, because the job
// that decides a verdict under the reusable workflows holds none.
func TestReviewStatusOutRendersTheDocumentInsteadOfPosting(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"})
	event, head := headEvent(t, dir, 7)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_SERVER_URL", "https://example.invalid")
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
	t.Setenv("GITHUB_RUN_ID", "12")

	out := filepath.Join(t.TempDir(), "referral", "status.json")
	report, err := runReview(t, dir, base, "--publish", "--status-out", out, "--event", event)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a change no exemption covers must be referred (exit 2), got %v:\n%s", err, report)
	}

	raw, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("reading the rendered status: %v", readErr)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the rendered status is not JSON: %v: %s", err, raw)
	}
	want := map[string]any{
		"state":        "pending",
		"context":      clearance.Context,
		"description":  "referred — comment /lydite clear",
		"target_url":   "https://example.invalid/lydite/lydite/actions/runs/12",
		"sha":          head,
		"pull_request": float64(7),
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %#v, want %#v", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the document carries %d field(s): %+v", len(got), got)
	}
}

// The rendered document and the direct post are two routes for one verdict,
// and a repository that has not adopted the reusable workflows takes the
// second — so what they say has to be the same, field for field.
func TestReviewPublishesTheSameVerdictItRenders(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"})
	event, head := headEvent(t, dir, 7)
	platform := &fakeForge{}
	platform.start(t)
	t.Setenv("GITHUB_SERVER_URL", "https://example.invalid")
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
	t.Setenv("GITHUB_RUN_ID", "12")

	if _, err := runReview(t, dir, base, "--publish", "--event", event); err == nil {
		t.Fatal("a change no exemption covers must be referred")
	}
	if len(platform.published) != 1 {
		t.Fatalf("published %d statuses, want 1", len(platform.published))
	}
	posted := platform.published[0]
	if posted["state"] != "pending" || posted["context"] != clearance.Context {
		t.Errorf("posted %+v, want a pending %s", posted, clearance.Context)
	}
	if posted["description"] != "referred — comment /lydite clear" {
		t.Errorf("description = %q", posted["description"])
	}
	if posted["target_url"] != "https://example.invalid/lydite/lydite/actions/runs/12" {
		t.Errorf("target_url = %q", posted["target_url"])
	}

	out := filepath.Join(t.TempDir(), "status.json")
	if _, err := runReview(t, dir, base, "--publish", "--status-out", out, "--event", event); err == nil {
		t.Fatal("a change no exemption covers must be referred")
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the rendered status: %v", err)
	}
	var rendered map[string]any
	if err := json.Unmarshal(raw, &rendered); err != nil {
		t.Fatalf("the rendered status is not JSON: %v: %s", err, raw)
	}
	for _, key := range []string{"state", "context", "description", "target_url"} {
		if rendered[key] != posted[key] {
			t.Errorf("%s: rendered %#v, posted %#v", key, rendered[key], posted[key])
		}
	}
	if rendered["sha"] != head {
		t.Errorf("sha = %#v, want the head the event names", rendered["sha"])
	}
	if len(platform.published) != 1 {
		t.Errorf("a rendered status was also posted: %+v", platform.published)
	}
}

// Rendering is the whole of the write, so a document that could not be written
// fails the run. A verdict that reached no file and no platform leaves the
// revision with no status, which reads on a pull request exactly like a gate
// that passed.
func TestReviewFailsWhenTheStatusDocumentCannotBeWritten(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"})
	event, _ := headEvent(t, dir, 7)
	occupied := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(occupied, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := runReview(t, dir, base, "--publish",
		"--status-out", filepath.Join(occupied, "status.json"), "--event", event)
	if err == nil {
		t.Fatalf("a status that could not be rendered was reported as published:\n%s", report)
	}
	var exit ui.ExitError
	if errors.As(err, &exit) {
		t.Errorf("a failed write must not answer as a verdict, got exit %d", exit.Code)
	}
	if !strings.Contains(err.Error(), "writing the status document") {
		t.Errorf("the failure must say what could not be written, got %v", err)
	}
}

// --status-out renders what --publish decides. On its own it names a
// destination nothing is ever written to, and a run that measured everything
// and wrote nowhere reports success — so it is refused, before any work.
func TestReviewRefusesAStatusOutWithoutPublish(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"})
	out := filepath.Join(t.TempDir(), "status.json")

	report, err := runReview(t, dir, base, "--status-out", out)
	if err == nil {
		t.Fatalf("--status-out on its own was accepted:\n%s", report)
	}
	if !strings.Contains(err.Error(), "--status-out") || !strings.Contains(err.Error(), "--publish") {
		t.Errorf("the refusal must name both flags, got %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("a refused run wrote a status document")
	}
}

// A status names a revision, and the revision comes from the event payload
// rather than the checkout — which on a pull_request run is a merge commit
// existing on no branch. With no event there is nothing to name, and rendering
// a document about a guessed revision is worse than refusing.
func TestReviewStatusOutRefusesWithoutAnEvent(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"})
	t.Setenv("GITHUB_EVENT_PATH", "")
	out := filepath.Join(t.TempDir(), "status.json")

	report, err := runReview(t, dir, base, "--publish", "--status-out", out)
	if err == nil {
		t.Fatalf("a status was rendered for no pull request:\n%s", report)
	}
	if !strings.Contains(err.Error(), "GITHUB_EVENT_PATH") {
		t.Errorf("the refusal must name what was missing, got %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("a refused run wrote a status document")
	}
}

// reviewFlowUncoveredRepo is a change no exemption covers, which every
// publishing run below would refer if it got as far as publishing.
func reviewFlowUncoveredRepo(t *testing.T) (dir, base string) {
	t.Helper()
	return reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"})
}

// reviewFlowNoReport fails the test when the run left a report behind: a
// verdict that was not published leaves no review.json and no stdout report,
// because either would be the only record a reader has and it would say the
// run finished.
func reviewFlowNoReport(t *testing.T, dir, out string) {
	t.Helper()
	if _, err := os.Stat(documentPath(reportsDir(dir), "review")); err == nil {
		t.Error("a run that did not publish wrote review.json")
	}
	if strings.Contains(out, "referral") {
		t.Errorf("a run that did not publish wrote its report:\n%s", out)
	}
}

// A run that posts holds a credential, and one with none is refused by the
// flag that needed it, in the words that say where a workflow grants it.
func TestReviewPublishWithNoTokenNamesTheToken(t *testing.T) {
	dir, base := reviewFlowUncoveredRepo(t)
	event, _ := headEvent(t, dir, 7)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")

	out, err := runReview(t, dir, base, "--publish", "--event", event)
	want := "--publish needs GITHUB_TOKEN (the workflow's `env:` block, with `statuses: write`)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q:\n%s", err, want, out)
	}
	reviewFlowNoReport(t, dir, out)
}

// A repository the platform did not name is refused in trust's own words, and
// nothing is posted: a credential with no repository to scope it to is not one
// lydite acts on.
func TestReviewPublishWithNoRepositoryPostsNothing(t *testing.T) {
	dir, base := reviewFlowUncoveredRepo(t)
	event, _ := headEvent(t, dir, 7)
	platform := &fakeForge{}
	platform.start(t)
	t.Setenv("GITHUB_REPOSITORY", "")

	out, err := runReview(t, dir, base, "--publish", "--event", event)
	if err == nil || !strings.Contains(err.Error(), "GITHUB_REPOSITORY is not set") {
		t.Fatalf("err = %v, want trust's refusal of a missing GITHUB_REPOSITORY:\n%s", err, out)
	}
	if platform.posts != 0 || len(platform.published) != 0 {
		t.Errorf("a run with no repository posted %d status(es): %+v", platform.posts, platform.published)
	}
	reviewFlowNoReport(t, dir, out)
}

// A rendered status names a revision too, and the render route's refusal names
// its own flag rather than one the run was never given.
func TestReviewStatusOutWithNoEventNamesTheStatusOutFlag(t *testing.T) {
	dir, base := reviewFlowUncoveredRepo(t)
	t.Setenv("GITHUB_EVENT_PATH", "")
	out := filepath.Join(t.TempDir(), "status.json")

	report, err := runReview(t, dir, base, "--publish", "--status-out", out)
	want := "--status-out needs GITHUB_EVENT_PATH: the head revision is read from the event, not from the checkout"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q:\n%s", err, want, report)
	}
}

// The direct route with everything but the event is refused by --publish, and
// posts nothing about a revision it could not name.
func TestReviewPublishWithNoEventNamesThePublishFlag(t *testing.T) {
	dir, base := reviewFlowUncoveredRepo(t)
	platform := &fakeForge{}
	platform.start(t)
	t.Setenv("GITHUB_EVENT_PATH", "")

	out, err := runReview(t, dir, base, "--publish")
	want := "--publish needs GITHUB_EVENT_PATH: the head revision is read from the event, not from the checkout"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q:\n%s", err, want, out)
	}
	if platform.posts != 0 {
		t.Errorf("a run with no event posted %d status(es)", platform.posts)
	}
	reviewFlowNoReport(t, dir, out)
}

// An event from some other trigger names no pull request, and the refusal says
// which trigger the flag belongs on.
func TestReviewStatusOutRefusesAnEventThatNamesNoPullRequest(t *testing.T) {
	dir, base := reviewFlowUncoveredRepo(t)
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"ref":"refs/heads/main"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "status.json")

	report, err := runReview(t, dir, base, "--publish", "--status-out", out, "--event", event)
	want := "the event at " + event + " names no pull request: --status-out belongs on a pull_request trigger"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q:\n%s", err, want, report)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("a refused run wrote a status document")
	}
}

// A post the platform refused is a failure, not a verdict, and leaves no
// report behind that would read as though the run had published.
func TestReviewFailedPostWritesNoReport(t *testing.T) {
	dir, base := reviewFlowUncoveredRepo(t)
	event, _ := headEvent(t, dir, 7)
	platform := &fakeForge{failPost: 1}
	platform.start(t)

	out, err := runReview(t, dir, base, "--publish", "--event", event)
	if err == nil {
		t.Fatalf("a refused post was reported as published:\n%s", out)
	}
	var exit ui.ExitError
	if errors.As(err, &exit) {
		t.Errorf("a failed post must not answer as a verdict, got exit %d", exit.Code)
	}
	if platform.posts != 1 || len(platform.published) != 0 {
		t.Errorf("posts = %d, published = %+v; want one refused post", platform.posts, platform.published)
	}
	reviewFlowNoReport(t, dir, out)
}
