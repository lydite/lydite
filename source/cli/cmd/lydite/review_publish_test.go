package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

func TestWriteVerdictRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdict.json")
	want := referral.Decision{Exemption: "docs-only", Empty: false}
	if err := writeVerdict(path, want, ui.VerdictRefer); err != nil {
		t.Fatalf("writeVerdict: %v", err)
	}

	got, verdict, err := readVerdict(path)
	if err != nil {
		t.Fatalf("readVerdict: %v", err)
	}
	if got.Exemption != want.Exemption || got.Empty != want.Empty || verdict != ui.VerdictRefer {
		t.Errorf("readVerdict = %+v, %q, want %+v, %q", got, verdict, want, ui.VerdictRefer)
	}
}

// A run that never touched the change under review still gets its own
// verdict, exemption and empty-change flag back out exactly as review wrote
// them — nothing about publishing needs to be recomputed to reach them.
func TestReviewPublishPostsAPrecomputedVerdictWithoutRecomputingAnything(t *testing.T) {
	forge := &fakeForge{}
	forge.start(t)
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")

	verdictPath := filepath.Join(t.TempDir(), "verdict.json")
	if err := writeVerdict(verdictPath, referral.Decision{Exemption: "docs-only"}, ui.VerdictRefer); err != nil {
		t.Fatalf("writeVerdict: %v", err)
	}

	event := reviewPullRequestEventFile(t, head)

	cmd := newReviewPublishCmd()
	cmd.SetArgs([]string{"--verdict", verdictPath, "--event", event})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("review publish: %v: %s", err, errOut.String())
	}

	if len(forge.published) != 1 {
		t.Fatalf("published %d statuses, want 1", len(forge.published))
	}
	if forge.published[0]["state"] != "pending" {
		t.Errorf(`published state = %q, want "pending" for a referral`, forge.published[0]["state"])
	}
	if want := "docs-only matched, then disqualified — comment /lydite clear"; forge.published[0]["description"] != want {
		t.Errorf("published description = %q, want %q", forge.published[0]["description"], want)
	}
}

func TestReviewPublishNeedsAVerdictFlag(t *testing.T) {
	cmd := newReviewPublishCmd()
	cmd.SetArgs(nil)
	var errOut bytes.Buffer
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err == nil {
		t.Error("review publish with no --verdict was accepted")
	}
}

// reviewPullRequestEventFile writes the pull_request webhook shape
// resolveTarget reads the head SHA from — distinct from clearance's own
// eventFile, which shapes a comment event instead.
func reviewPullRequestEventFile(t *testing.T, sha string) string {
	t.Helper()
	payload := map[string]any{
		"number": 40,
		"pull_request": map[string]any{
			"title": "docs: note the exemption",
			"head":  map[string]any{"sha": sha},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
