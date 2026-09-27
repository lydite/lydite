package scmstages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/trust"
)

// fakeRepository is a forge.SCMRepository whose every method fails the test
// if called without a function supplied for it — a stage calling a method
// nothing in the test case expects is itself the bug under test.
type fakeRepository struct {
	t *testing.T

	issueComment   func(ctx context.Context, id int64) (forge.IssueComment, error)
	headSHA        func(ctx context.Context, number int) (string, error)
	canWrite       func(ctx context.Context, user string) (bool, error)
	referralStatus func(ctx context.Context, sha string) (*clearance.Status, error)
	postStatus     func(ctx context.Context, s forge.Status) error
	createComment  func(ctx context.Context, number int, body string) error
}

func (f *fakeRepository) IssueComment(ctx context.Context, id int64) (forge.IssueComment, error) {
	if f.issueComment == nil {
		f.t.Fatal("IssueComment: unexpected call")
	}
	return f.issueComment(ctx, id)
}

func (f *fakeRepository) HeadSHA(ctx context.Context, number int) (string, error) {
	if f.headSHA == nil {
		f.t.Fatal("HeadSHA: unexpected call")
	}
	return f.headSHA(ctx, number)
}

func (f *fakeRepository) PullRequestTitle(_ context.Context, _ int) (string, error) {
	f.t.Fatal("PullRequestTitle: unexpected call")
	return "", nil
}

func (f *fakeRepository) ChangedPaths(_ context.Context, _ int) ([]string, error) {
	f.t.Fatal("ChangedPaths: unexpected call")
	return nil, nil
}

func (f *fakeRepository) CanWrite(ctx context.Context, user string) (bool, error) {
	if f.canWrite == nil {
		f.t.Fatal("CanWrite: unexpected call")
	}
	return f.canWrite(ctx, user)
}

func (f *fakeRepository) ReferralStatus(ctx context.Context, sha string) (*clearance.Status, error) {
	if f.referralStatus == nil {
		f.t.Fatal("ReferralStatus: unexpected call")
	}
	return f.referralStatus(ctx, sha)
}

func (f *fakeRepository) PostStatus(ctx context.Context, s forge.Status) error {
	if f.postStatus == nil {
		f.t.Fatal("PostStatus: unexpected call")
	}
	return f.postStatus(ctx, s)
}

func (f *fakeRepository) CreateComment(ctx context.Context, number int, body string) error {
	if f.createComment == nil {
		f.t.Fatal("CreateComment: unexpected call")
	}
	return f.createComment(ctx, number, body)
}

// trustedContext builds a trust.TrustedContext for a test the way trust's own
// tests do: TrustedContext is sealed, so FromEnvironment reading environment
// variables set for the test is the only route to one outside the package.
func trustedContext(t *testing.T, repository, token string) trust.TrustedContext {
	t.Helper()
	t.Setenv("GITHUB_REPOSITORY", repository)
	t.Setenv("GITHUB_TOKEN", token)
	t.Setenv("GH_TOKEN", "")
	trusted, err := trust.FromEnvironment()
	if err != nil {
		t.Fatalf("trust.FromEnvironment: %v", err)
	}
	return trusted
}

func TestInitSCMRefusesNoWriteCredential(t *testing.T) {
	trusted := trustedContext(t, "owner/name", "")
	_, err := InitSCM(context.Background(), InitSCMIn{Trust: trusted})
	if !errors.Is(err, ErrNoCredential) {
		t.Errorf("InitSCM error = %v; want %v", err, ErrNoCredential)
	}
}

func TestInitSCMBuildsARepositoryForATrustedContextThatCanWrite(t *testing.T) {
	trusted := trustedContext(t, "owner/name", "a-token")
	out, err := InitSCM(context.Background(), InitSCMIn{Trust: trusted})
	if err != nil {
		t.Fatalf("InitSCM: %v", err)
	}
	if out.Repository == nil {
		t.Fatal("InitSCM built no repository")
	}
	gh, ok := out.Repository.(*forge.GitHubRepository)
	if !ok {
		t.Fatalf("InitSCM built a %T, not a *forge.GitHubRepository", out.Repository)
	}
	if gh.Repo.Owner != "owner" || gh.Repo.Name != "name" {
		t.Errorf("InitSCM built a repository for %s/%s; want owner/name", gh.Repo.Owner, gh.Repo.Name)
	}
}

func TestLoadCommentRefusesAForeignRepository(t *testing.T) {
	trusted := trustedContext(t, "owner/name", "a-token")
	repo := &fakeRepository{t: t}
	_, err := LoadComment(context.Background(), LoadCommentIn{
		Trust:      trusted,
		Repository: repo,
		Ref:        forge.CommentRef{ID: 1, Repository: "someone-else/other"},
	})
	if err == nil {
		t.Fatal("LoadComment with a foreign repository succeeded")
	}
}

func TestLoadCommentAcceptsACaseDifferingSameRepository(t *testing.T) {
	trusted := trustedContext(t, "Owner/Name", "a-token")
	want := forge.IssueComment{Body: "hello", Number: 7, OnPullRequest: true}
	repo := &fakeRepository{
		t: t,
		issueComment: func(_ context.Context, id int64) (forge.IssueComment, error) {
			if id != 1 {
				t.Errorf("IssueComment called with id %d; want 1", id)
			}
			return want, nil
		},
	}
	out, err := LoadComment(context.Background(), LoadCommentIn{
		Trust:      trusted,
		Repository: repo,
		Ref:        forge.CommentRef{ID: 1, Repository: "owner/name"},
	})
	if err != nil {
		t.Fatalf("LoadComment: %v", err)
	}
	if out.Comment != want {
		t.Errorf("LoadComment out = %+v; want %+v", out.Comment, want)
	}
}

func TestLoadCommentPropagatesTheFetchError(t *testing.T) {
	trusted := trustedContext(t, "owner/name", "a-token")
	fetchErr := errors.New("boom")
	repo := &fakeRepository{
		t: t,
		issueComment: func(_ context.Context, _ int64) (forge.IssueComment, error) {
			return forge.IssueComment{}, fetchErr
		},
	}
	_, err := LoadComment(context.Background(), LoadCommentIn{
		Trust:      trusted,
		Repository: repo,
		Ref:        forge.CommentRef{ID: 1, Repository: "owner/name"},
	})
	if !errors.Is(err, fetchErr) {
		t.Errorf("LoadComment error = %v; want %v", err, fetchErr)
	}
}

func TestResolveHead(t *testing.T) {
	repo := &fakeRepository{
		t: t,
		headSHA: func(_ context.Context, number int) (string, error) {
			if number != 40 {
				t.Errorf("HeadSHA called with %d; want 40", number)
			}
			return "abc123", nil
		},
	}
	out, err := ResolveHead(context.Background(), ResolveHeadIn{Repository: repo, Number: 40})
	if err != nil {
		t.Fatalf("ResolveHead: %v", err)
	}
	if out.SHA != "abc123" {
		t.Errorf("ResolveHead SHA = %q; want %q", out.SHA, "abc123")
	}
}

func TestResolveHeadPropagatesTheError(t *testing.T) {
	headErr := errors.New("boom")
	repo := &fakeRepository{
		t:       t,
		headSHA: func(_ context.Context, _ int) (string, error) { return "", headErr },
	}
	_, err := ResolveHead(context.Background(), ResolveHeadIn{Repository: repo, Number: 40})
	if !errors.Is(err, headErr) {
		t.Errorf("ResolveHead error = %v; want %v", err, headErr)
	}
}

func TestCheckPermission(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "may write", want: true},
		{name: "may not write", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{
				t: t,
				canWrite: func(_ context.Context, user string) (bool, error) {
					if user != "octocat" {
						t.Errorf("CanWrite called with %q; want %q", user, "octocat")
					}
					return tc.want, nil
				},
			}
			out, err := CheckPermission(context.Background(), CheckPermissionIn{Repository: repo, Login: "octocat"})
			if err != nil {
				t.Fatalf("CheckPermission: %v", err)
			}
			if out.CanWrite != tc.want {
				t.Errorf("CheckPermission CanWrite = %t; want %t", out.CanWrite, tc.want)
			}
		})
	}
}

func TestCheckPermissionPropagatesTheError(t *testing.T) {
	permErr := errors.New("boom")
	repo := &fakeRepository{
		t:        t,
		canWrite: func(_ context.Context, _ string) (bool, error) { return false, permErr },
	}
	_, err := CheckPermission(context.Background(), CheckPermissionIn{Repository: repo, Login: "octocat"})
	if !errors.Is(err, permErr) {
		t.Errorf("CheckPermission error = %v; want %v", err, permErr)
	}
}

func TestReadStatusNoStandingStatus(t *testing.T) {
	repo := &fakeRepository{
		t: t,
		referralStatus: func(_ context.Context, sha string) (*clearance.Status, error) {
			if sha != "abc123" {
				t.Errorf("ReferralStatus called with %q; want %q", sha, "abc123")
			}
			return nil, nil
		},
	}
	out, err := ReadStatus(context.Background(), ReadStatusIn{Repository: repo, SHA: "abc123"})
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if out.Status != nil {
		t.Errorf("ReadStatus Status = %+v; want nil", out.Status)
	}
}

func TestReadStatusAStandingStatus(t *testing.T) {
	want := &clearance.Status{State: clearance.StateSuccess, Description: "cleared"}
	repo := &fakeRepository{
		t:              t,
		referralStatus: func(_ context.Context, _ string) (*clearance.Status, error) { return want, nil },
	}
	out, err := ReadStatus(context.Background(), ReadStatusIn{Repository: repo, SHA: "abc123"})
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if out.Status != want {
		t.Errorf("ReadStatus Status = %+v; want %+v", out.Status, want)
	}
}

func TestReadStatusPropagatesTheError(t *testing.T) {
	statusErr := errors.New("boom")
	repo := &fakeRepository{
		t:              t,
		referralStatus: func(_ context.Context, _ string) (*clearance.Status, error) { return nil, statusErr },
	}
	_, err := ReadStatus(context.Background(), ReadStatusIn{Repository: repo, SHA: "abc123"})
	if !errors.Is(err, statusErr) {
		t.Errorf("ReadStatus error = %v; want %v", err, statusErr)
	}
}

func TestPostComment(t *testing.T) {
	var gotNumber int
	var gotBody string
	repo := &fakeRepository{
		t: t,
		createComment: func(_ context.Context, number int, body string) error {
			gotNumber, gotBody = number, body
			return nil
		},
	}
	if _, err := PostComment(context.Background(), PostCommentIn{Repository: repo, Number: 40, Body: "hello"}); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotNumber != 40 || gotBody != "hello" {
		t.Errorf("PostComment posted (%d, %q); want (40, %q)", gotNumber, gotBody, "hello")
	}
}

func TestPostCommentPropagatesTheError(t *testing.T) {
	postErr := errors.New("boom")
	repo := &fakeRepository{
		t:             t,
		createComment: func(_ context.Context, _ int, _ string) error { return postErr },
	}
	_, err := PostComment(context.Background(), PostCommentIn{Repository: repo, Number: 40, Body: "hello"})
	if !errors.Is(err, postErr) {
		t.Errorf("PostComment error = %v; want %v", err, postErr)
	}
}

// eventFile writes payload as an event file and returns its path.
func eventFile(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPullRequestReadsThePointerThePayloadCarries(t *testing.T) {
	path := eventFile(t, `{"number": 7, "pull_request": {"number": 7, "title": "feat!: drop v1", "head": {"sha": "abc123"}}}`)
	out, err := LoadPullRequest(context.Background(), LoadPullRequestIn{EventPath: path})
	if err != nil {
		t.Fatalf("LoadPullRequest: %v", err)
	}
	want := forge.PullRequestRef{Number: 7, SHA: "abc123", Title: "feat!: drop v1"}
	if out.Ref != want {
		t.Errorf("LoadPullRequest Ref = %+v; want %+v", out.Ref, want)
	}
}

func TestLoadPullRequestPassesNoEventThroughTyped(t *testing.T) {
	_, err := LoadPullRequest(context.Background(), LoadPullRequestIn{})
	if !errors.Is(err, forge.ErrNoEvent) {
		t.Errorf("LoadPullRequest error = %v; want %v", err, forge.ErrNoEvent)
	}
}

func TestLoadPullRequestPassesAPayloadNamingNoPullRequestThroughTyped(t *testing.T) {
	path := eventFile(t, `{"action": "checks_requested"}`)
	_, err := LoadPullRequest(context.Background(), LoadPullRequestIn{EventPath: path})
	var notAPullRequest *forge.NotAPullRequestError
	if !errors.As(err, &notAPullRequest) {
		t.Fatalf("LoadPullRequest error = %v; want a *forge.NotAPullRequestError", err)
	}
	if notAPullRequest.Path != path {
		t.Errorf("NotAPullRequestError Path = %q; want %q", notAPullRequest.Path, path)
	}
}

func TestLoadPullRequestPropagatesALoadError(t *testing.T) {
	_, err := LoadPullRequest(context.Background(), LoadPullRequestIn{EventPath: filepath.Join(t.TempDir(), "absent.json")})
	if err == nil || errors.Is(err, forge.ErrNoEvent) {
		t.Fatalf("LoadPullRequest error = %v; want the read failure", err)
	}
	var notAPullRequest *forge.NotAPullRequestError
	if errors.As(err, &notAPullRequest) {
		t.Errorf("LoadPullRequest error = %v; a missing file is not a payload naming no pull request", err)
	}
}

func TestPostStatus(t *testing.T) {
	want := forge.Status{State: clearance.StatePending, Context: clearance.Context, Description: "referred", SHA: "abc123", PullRequest: 40}
	var got []forge.Status
	repo := &fakeRepository{
		t: t,
		postStatus: func(_ context.Context, s forge.Status) error {
			got = append(got, s)
			return nil
		},
	}
	if _, err := PostStatus(context.Background(), PostStatusIn{Repository: repo, Status: want}); err != nil {
		t.Fatalf("PostStatus: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("PostStatus posted %+v; want exactly %+v", got, want)
	}
}

func TestPostStatusPropagatesTheError(t *testing.T) {
	postErr := errors.New("boom")
	repo := &fakeRepository{
		t:          t,
		postStatus: func(_ context.Context, _ forge.Status) error { return postErr },
	}
	_, err := PostStatus(context.Background(), PostStatusIn{Repository: repo, Status: forge.Status{SHA: "abc123"}})
	if !errors.Is(err, postErr) {
		t.Errorf("PostStatus error = %v; want %v", err, postErr)
	}
}
