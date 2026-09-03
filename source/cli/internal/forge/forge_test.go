package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/clearance"
)

func serve(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := New("token")
	client.BaseURL = server.URL
	return client
}

var repo = Repo{Owner: "lydite", Name: "lydite"}

func TestParseRepoRejectsAnythingButOwnerAndName(t *testing.T) {
	if _, err := ParseRepo("lydite"); err == nil {
		t.Error("a bare name parsed as a repository")
	}
	got, err := ParseRepo("lydite/lydite")
	if err != nil || got != repo {
		t.Fatalf("ParseRepo = %+v, %v", got, err)
	}
}

// The platform returns statuses newest first, so what is in force is the
// first entry under the context. Reading further back would find the pending
// verdict underneath every clearance and report a cleared change as referred.
func TestReferralStatusTakesTheNewestUnderTheContext(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"state": "success", "context": clearance.Context, "description": "cleared", "created_at": now},
			{"state": "failure", "context": "ci-gate", "created_at": now},
			{"state": "pending", "context": clearance.Context, "description": "referred", "created_at": now.Add(-time.Hour)},
		})
	})
	got, err := client.ReferralStatus(context.Background(), repo, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.State != clearance.StateSuccess {
		t.Fatalf("got %+v, want the newest lydite status", got)
	}
}

// A revision nothing has decided about is an answer, not a failure.
func TestReferralStatusIsNilWhenNoLyditeStatusStands(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"state": "success", "context": "ci-gate", "created_at": time.Now()},
		})
	})
	got, err := client.ReferralStatus(context.Background(), repo, "abc123")
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v; want nil, nil", got, err)
	}
}

// A user who is not a collaborator answers 404, which is the ordinary case
// on a public repository. Treating it as an error would refuse every
// stranger with a message about a broken platform.
func TestCanWriteTreatsANonCollaboratorAsNoPermission(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	ok, err := client.CanWrite(context.Background(), repo, "stranger")
	if err != nil || ok {
		t.Fatalf("CanWrite = %v, %v; want false, nil", ok, err)
	}
}

func TestCanWriteAcceptsOnlyPushingPermissions(t *testing.T) {
	for permission, want := range map[string]bool{
		"admin": true, "maintain": true, "write": true, "triage": false, "read": false, "none": false,
	} {
		client := serve(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"permission":"` + permission + `"}`))
		})
		got, err := client.CanWrite(context.Background(), repo, "someone")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("permission %q = %v, want %v", permission, got, want)
		}
	}
}

func TestPublishStatusSendsTheContextAndState(t *testing.T) {
	var body map[string]string
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	})
	if err := client.PublishStatus(context.Background(), repo, "abc123", clearance.StatePending, "referred", ""); err != nil {
		t.Fatal(err)
	}
	if body["context"] != clearance.Context || body["state"] != "pending" {
		t.Fatalf("body = %+v", body)
	}
}

// The platform counts a description in characters and rejects an over-long
// one, so a verdict carrying a long path must still publish.
func TestPublishStatusKeepsTheDescriptionInsideTheLimit(t *testing.T) {
	var body map[string]string
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	})
	long := strings.Repeat("é", 400)
	if err := client.PublishStatus(context.Background(), repo, "abc", clearance.StateFailure, long, ""); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(body["description"])); n > 140 {
		t.Fatalf("description is %d characters, want at most 140", n)
	}
	if !strings.HasSuffix(body["description"], "…") {
		t.Error("a truncated description should show that it was cut")
	}
}

// The sticky comment is found by a marker in its body rather than by author,
// because the author is whoever's token the workflow runs under.
func TestUpsertEditsTheCommentCarryingTheMarker(t *testing.T) {
	const marker = "<!-- lydite:referral -->"
	var patched string
	var created bool
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]Comment{
				{ID: 1, Body: "unrelated"},
				{ID: 2, Body: marker + "\nreferred"},
			})
		case http.MethodPatch:
			if !strings.HasSuffix(r.URL.Path, "/comments/2") {
				t.Errorf("patched %s, want comment 2", r.URL.Path)
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			patched = body["body"]
		case http.MethodPost:
			created = true
		}
	})
	if err := client.UpsertComment(context.Background(), repo, 40, marker, marker+"\ncleared"); err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("a second comment was created instead of editing the standing one")
	}
	if !strings.Contains(patched, "cleared") {
		t.Fatalf("patched body = %q", patched)
	}
}

func TestUpsertCreatesWhenNoCommentCarriesTheMarker(t *testing.T) {
	var created bool
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]Comment{{ID: 1, Body: "unrelated"}})
			return
		}
		created = true
		w.WriteHeader(http.StatusCreated)
	})
	if err := client.UpsertComment(context.Background(), repo, 40, "<!-- m -->", "body"); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("no comment was created")
	}
}

// The platform's own message names the missing scope, which is the most
// likely thing to be wrong and the least guessable from a bare status code.
func TestAnAPIErrorCarriesThePlatformsMessage(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	})
	err := client.PublishStatus(context.Background(), repo, "abc", clearance.StatePending, "referred", "")
	if err == nil || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("err = %v, want the platform's message", err)
	}
}

func TestLoadCommentEventReadsTheWebhookPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	payload := `{
	  "action": "created",
	  "issue": {"number": 40, "pull_request": {"url": "https://api.github.com/repos/lydite/lydite/pulls/40"}},
	  "comment": {"body": "/lydite clear", "created_at": "2026-08-31T12:00:00Z", "user": {"login": "pedromvgomes"}},
	  "repository": {"full_name": "lydite/lydite"}
	}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	event, err := LoadCommentEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	if !event.OnPullRequest() {
		t.Error("a comment on a pull request was read as an issue comment")
	}
	if event.Issue.Number != 40 || event.Comment.User.Login != "pedromvgomes" {
		t.Fatalf("event = %+v", event)
	}
	if !event.Comment.CreatedAt.Equal(time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("created_at = %v", event.Comment.CreatedAt)
	}
}

// A comment on a plain issue names no revision, so there is nothing to
// decide about it.
func TestACommentOnAnIssueIsNotOnAPullRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"issue":{"number":7}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	event, err := LoadCommentEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	if event.OnPullRequest() {
		t.Error("an issue comment was read as a pull-request comment")
	}
}
