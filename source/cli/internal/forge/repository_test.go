package forge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/flow"
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

func initializeSCM(t *testing.T, r Repo) (*flow.Context, error) {
	t.Helper()
	c := flow.NewContext()
	f := flow.Flow{Stages: []flow.Stage{
		{Name: "trust", Components: []flow.StageComponent{trust.InitializeTrustContext{}}},
		{Name: "scm", Components: []flow.StageComponent{InitializeSCM{Repo: r}}},
	}}
	return c, f.Run(context.Background(), c)
}

func TestInitializeSCMJoinsARepositoryBuiltFromTheToken(t *testing.T) {
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
			t.Setenv("GITHUB_TOKEN", tc.githubToken)
			t.Setenv("GH_TOKEN", tc.ghToken)
			c, err := initializeSCM(t, repo)
			if err != nil {
				t.Fatalf("Flow.Run: %v", err)
			}
			scm, ok := Get(c)
			if !ok {
				t.Fatal("no SCMRepository joined after InitializeSCM ran")
			}
			gh, ok := scm.(*GitHubRepository)
			if !ok {
				t.Fatalf("joined %T, want *GitHubRepository", scm)
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

// Clearance posts statuses and comments; a run with no credential has
// nothing it can do, so the Flow fails with the message naming what it needs.
func TestInitializeSCMFailsTheFlowWithoutAToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	c, err := initializeSCM(t, repo)
	if err == nil {
		t.Fatal("Flow.Run succeeded with no token")
	}
	const message = "clearance needs GITHUB_TOKEN with `statuses: write` and `pull-requests: write`"
	if !strings.Contains(err.Error(), message) {
		t.Errorf("error %q does not carry %q", err, message)
	}
	if _, ok := Get(c); ok {
		t.Error("an SCMRepository was joined with no token")
	}

	result, err := InitializeSCM{Repo: repo}.Run(context.Background(), c.View())
	if !errors.Is(err, errNoToken) {
		t.Errorf("Run error = %v, want errNoToken", err)
	}
	if result.Policy != flow.FailFlow {
		t.Errorf("Policy = %v, want %v", result.Policy, flow.FailFlow)
	}
}

// A Context no trust stage has run on answers as holding no credential, even
// when the environment carries a token.
func TestInitializeSCMRefusesAContextWithNoTrustDecision(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "primary")
	result, err := InitializeSCM{Repo: repo}.Run(context.Background(), flow.NewContext().View())
	if !errors.Is(err, errNoToken) {
		t.Errorf("Run error = %v, want errNoToken", err)
	}
	if result.Policy != flow.FailFlow || len(result.Writes) != 0 {
		t.Errorf("Result = %+v, want FailFlow with no writes", result)
	}
}

func TestGetReportsAbsentBeforeInitialization(t *testing.T) {
	if _, ok := Get(flow.NewContext()); ok {
		t.Fatal("Get on a Context no stage initialized reported present")
	}
}
