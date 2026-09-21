package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
)

// The document is read by a step that posts it and by nothing else, so every
// field it carries is pinned by name and by value — including the pull
// request, which is what places a verdict for a poster whose own claims name
// no pull ref.
func TestWriteStatusRendersEveryFieldTheStepPosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	err := WriteStatus(path, Status{
		State:       clearance.StatePending,
		Context:     clearance.Context,
		Description: "referred — comment /lydite clear",
		TargetURL:   "https://example.invalid/actions/runs/7",
		SHA:         "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091",
		PullRequest: 40,
	})
	if err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	got := readStatus(t, path)
	want := map[string]any{
		"state":        "pending",
		"context":      clearance.Context,
		"description":  "referred — comment /lydite clear",
		"target_url":   "https://example.invalid/actions/runs/7",
		"sha":          "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091",
		"pull_request": float64(40),
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %#v, want %#v", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("document carries %d field(s): %+v", len(got), got)
	}
}

// A description over the platform's limit is refused, and the refusal would
// land in a step that decided nothing — so the document carries the text a
// direct post would have written.
func TestWriteStatusClipsTheDescriptionToThePlatformsLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := WriteStatus(path, Status{Description: strings.Repeat("é", 400)}); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	description, _ := readStatus(t, path)["description"].(string)
	if n := len([]rune(description)); n != statusDescriptionLimit {
		t.Errorf("description is %d characters, want %d", n, statusDescriptionLimit)
	}
	if !strings.HasSuffix(description, "…") {
		t.Errorf("a clipped description should show that it was cut, got %q", description)
	}
}

// A local run has no run to point at, and an empty target_url posted verbatim
// is a status linking nowhere. The key is absent instead.
func TestWriteStatusOmitsAnAbsentTargetURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := WriteStatus(path, Status{State: clearance.StateSuccess, SHA: "abc"}); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	if _, ok := readStatus(t, path)["target_url"]; ok {
		t.Error("an empty target URL was rendered as a field")
	}
}

// The caller names a path under a directory that does not exist yet — an
// artifact directory a later step uploads — and rendering it is what creates
// that directory.
func TestWriteStatusCreatesTheDirectoryItWritesInto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports", "referral", "status.json")
	if err := WriteStatus(path, Status{State: clearance.StatePending, SHA: "abc", PullRequest: 1}); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	if got := readStatus(t, path)["sha"]; got != "abc" {
		t.Errorf("sha = %#v, want abc", got)
	}
}

// A document that could not be written is an error and never a shrug: the
// revision is left with no status, which on a pull request reads exactly like
// a gate that passed.
func TestWriteStatusFailsWhenThePathCannotBeWritten(t *testing.T) {
	file := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteStatus(filepath.Join(file, "status.json"), Status{State: clearance.StatePending, SHA: "abc"})
	if err == nil {
		t.Fatal("a status document that could not be written was reported as written")
	}
	if !strings.Contains(err.Error(), "writing the status document") {
		t.Errorf("the failure must say what could not be written, got %v", err)
	}
}

// PostStatus writes the document's own context and state rather than assuming
// either, so one shape serves the referral verdict and a clearance alike.
func TestPostStatusWritesTheDocumentItWasGiven(t *testing.T) {
	var body map[string]string
	var path string
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	})
	err := client.PostStatus(context.Background(), repo, Status{
		State:       clearance.StateSuccess,
		Context:     "lydite/clearance",
		Description: "cleared by someone",
		TargetURL:   "https://example.invalid/run",
		SHA:         "abc123",
		PullRequest: 40,
	})
	if err != nil {
		t.Fatalf("PostStatus: %v", err)
	}
	if !strings.HasSuffix(path, "/statuses/abc123") {
		t.Errorf("posted to %q, want the revision the document names", path)
	}
	want := map[string]string{
		"state":       "success",
		"context":     "lydite/clearance",
		"description": "cleared by someone",
		"target_url":  "https://example.invalid/run",
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("%s = %q, want %q", key, body[key], value)
		}
	}
	// A commit status belongs to a revision; the pull request the document
	// also names places the verdict for a step posting under another
	// identity and is not part of what the platform takes here.
	if _, ok := body["pull_request"]; ok {
		t.Errorf("body carries a pull request the platform does not take: %+v", body)
	}
}

func readStatus(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the status document: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("the document should end in a newline")
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the status document is not JSON: %v: %s", err, raw)
	}
	return got
}
