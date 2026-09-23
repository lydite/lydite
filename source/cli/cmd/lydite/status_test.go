package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
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

// A clearance's fingerprint lives in the status description and nowhere else,
// and forge clips that description to the platform's cap on the way out — so
// the fingerprint has to survive the write and not merely the composition. Both
// documents a clearance records carry it, because both are written from one
// document.
func TestARecordedClearanceCarriesTheFingerprintThroughTheWrite(t *testing.T) {
	headSHA := "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091"
	want := referral.Fingerprint([]string{"src/auth.go"}, nil)
	// Longer than any login the platform issues: the attribution is what gives
	// way to the budget, and asserting that needs a handle that spends it.
	handle := strings.Repeat("handle", 40)
	description := clearance.WithFingerprint(
		fmt.Sprintf("cleared by @%s at %s", handle, shortSHA(headSHA)), want)

	out := filepath.Join(t.TempDir(), "lydite-status.json")
	if err := recordClearance(context.Background(), nil, forge.Repo{}, out,
		clearanceStatus(pullRequestRef{SHA: headSHA, Number: 7}, description)); err != nil {
		t.Fatalf("recording the clearance: %v", err)
	}

	for _, path := range []string{out, referralDocument(out)} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading the recorded status: %v", err)
		}
		var got struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("the recorded status is not JSON: %v: %s", err, raw)
		}
		if n := len([]rune(got.Description)); n > clearance.DescriptionLimit {
			t.Errorf("%s: the description is %d characters, past the cap of %d: %q",
				filepath.Base(path), n, clearance.DescriptionLimit, got.Description)
		}
		fingerprint, ok := clearance.FingerprintIn(got.Description)
		if !ok || fingerprint != want {
			t.Errorf("%s: the recorded description reads back as %q, %v; want %q, true",
				filepath.Base(path), fingerprint, ok, want)
		}
	}
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
