package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/trust"
)

type request struct {
	method string
	path   string
	body   string
}

// Every SCMRepository method is the Client call of the same name against the
// repository it is bound to: the same verb, the same path, the same body.
func TestGitHubRepositoryDelegatesEveryCallToTheClient(t *testing.T) {
	var got []request
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, request{method: r.Method, path: r.URL.Path, body: string(body)})
		switch {
		case strings.Contains(r.URL.Path, "/issues/comments/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"body": "/lydite clear", "created_at": "2026-08-31T12:00:00Z",
				"issue_url": "https://api.github.com/repos/lydite/lydite/issues/7",
				"user":      map[string]string{"login": "octocat"},
			})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/issues/7"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "pull_request": map[string]string{"url": "x"}})
		case strings.HasSuffix(r.URL.Path, "/files"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "a.go", "status": "modified"}})
		case strings.HasSuffix(r.URL.Path, "/statuses"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"state": "pending", "context": clearance.Context}})
		case strings.HasSuffix(r.URL.Path, "/permission"):
			_ = json.NewEncoder(w).Encode(map[string]string{"permission": "write"})
		case strings.Contains(r.URL.Path, "/pulls/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"title": "fix: x", "head": map[string]string{"sha": "abc123"}})
		default:
			w.WriteHeader(http.StatusCreated)
		}
	})
	var scm SCMRepository = &GitHubRepository{Client: client, Repo: repo}
	ctx := context.Background()

	if c, err := scm.IssueComment(ctx, 55); err != nil || c.Author != "octocat" || c.Number != 7 || !c.OnPullRequest {
		t.Errorf("IssueComment = %+v, %v", c, err)
	}
	if head, err := scm.HeadSHA(ctx, 7); err != nil || head != "abc123" {
		t.Errorf("HeadSHA = %q, %v", head, err)
	}
	if title, err := scm.PullRequestTitle(ctx, 7); err != nil || title != "fix: x" {
		t.Errorf("PullRequestTitle = %q, %v", title, err)
	}
	if paths, err := scm.ChangedPaths(ctx, 7); err != nil || len(paths) != 1 || paths[0] != "a.go" {
		t.Errorf("ChangedPaths = %v, %v", paths, err)
	}
	if ok, err := scm.CanWrite(ctx, "octocat"); err != nil || !ok {
		t.Errorf("CanWrite = %t, %v", ok, err)
	}
	if s, err := scm.ReferralStatus(ctx, "abc123"); err != nil || s == nil || s.State != clearance.StatePending {
		t.Errorf("ReferralStatus = %+v, %v", s, err)
	}
	status := Status{State: clearance.StateSuccess, Context: clearance.Context, Description: "cleared", SHA: "abc123", PullRequest: 7}
	if err := scm.PostStatus(ctx, status); err != nil {
		t.Errorf("PostStatus: %v", err)
	}
	if err := scm.CreateComment(ctx, 7, "hello"); err != nil {
		t.Errorf("CreateComment: %v", err)
	}

	want := []request{
		{method: "GET", path: "/repos/lydite/lydite/issues/comments/55"},
		{method: "GET", path: "/repos/lydite/lydite/issues/7"},
		{method: "GET", path: "/repos/lydite/lydite/pulls/7"},
		{method: "GET", path: "/repos/lydite/lydite/pulls/7"},
		{method: "GET", path: "/repos/lydite/lydite/pulls/7/files"},
		{method: "GET", path: "/repos/lydite/lydite/collaborators/octocat/permission"},
		{method: "GET", path: "/repos/lydite/lydite/commits/abc123/statuses"},
		{method: "POST", path: "/repos/lydite/lydite/statuses/abc123"},
		{method: "POST", path: "/repos/lydite/lydite/issues/7/comments", body: `{"body":"hello"}`},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d requests %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if got[i].method != w.method || got[i].path != w.path {
			t.Errorf("request %d = %s %s, want %s %s", i, got[i].method, got[i].path, w.method, w.path)
		}
		if w.body != "" && strings.TrimSpace(got[i].body) != w.body {
			t.Errorf("request %d body = %s, want %s", i, got[i].body, w.body)
		}
	}
}

func trusted(t *testing.T, repository, githubToken, ghToken string) trust.TrustedContext {
	t.Helper()
	t.Setenv("GITHUB_REPOSITORY", repository)
	t.Setenv("GITHUB_TOKEN", githubToken)
	t.Setenv("GH_TOKEN", ghToken)
	tc, err := trust.FromEnvironment()
	if err != nil {
		t.Fatalf("trust.FromEnvironment: %v", err)
	}
	return tc
}

// The repository every call is made against and the credential it is made
// with are the TrustedContext's, whichever variable the credential came from.
func TestNewGitHubRepositoryIsBoundToTheTrustedRepositoryAndToken(t *testing.T) {
	cases := []struct {
		name        string
		githubToken string
		ghToken     string
		want        string
	}{
		{name: "GITHUB_TOKEN", githubToken: "primary", want: "primary"},
		{name: "GH_TOKEN", ghToken: "fallback", want: "fallback"},
		{name: "GITHUB_TOKEN first", githubToken: "primary", ghToken: "fallback", want: "primary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh, err := NewGitHubRepository(trusted(t, "lydite/lydite", tc.githubToken, tc.ghToken))
			if err != nil {
				t.Fatalf("NewGitHubRepository: %v", err)
			}
			if gh.Repo != repo {
				t.Errorf("Repo = %v, want %v", gh.Repo, repo)
			}
			if gh.Client == nil || gh.Client.Token != tc.want {
				t.Errorf("Client = %+v, want token %q", gh.Client, tc.want)
			}
		})
	}
}

// Whether a run may write is the TrustedContext's answer, not this
// constructor's: one holding no credential still binds a repository, with a
// client that sends no Authorization header.
func TestNewGitHubRepositoryWithoutACredentialBindsAnUnauthenticatedClient(t *testing.T) {
	tc := trusted(t, "lydite/lydite", "", "")
	gh, err := NewGitHubRepository(tc)
	if err != nil {
		t.Fatalf("NewGitHubRepository: %v", err)
	}
	if gh.Repo != repo || gh.Client == nil || gh.Client.Token != "" {
		t.Errorf("GitHubRepository = %+v, want %v with no token", gh, repo)
	}
}

// The zero TrustedContext is the only one outside internal/trust that
// FromEnvironment did not build, and it names no repository to bind.
func TestNewGitHubRepositoryRefusesATrustedContextNamingNoRepository(t *testing.T) {
	gh, err := NewGitHubRepository(trust.TrustedContext{})
	if err == nil {
		t.Fatalf("NewGitHubRepository = %+v, want a refusal", gh)
	}
	if !strings.Contains(err.Error(), "the trusted repository") {
		t.Errorf("err = %v, want one naming the trusted repository", err)
	}
}
