package forge

import (
	"context"
	"errors"
	"os"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/trust"
)

// SCMRepository is one repository on the hosting platform, as the clearance
// workflow reads and writes it.
//
// Every method is typed in lydite's own terms — a pull request is an int, a
// status is this package's Status, a standing verdict is clearance.Status —
// so nothing holding one depends on the platform's own payload shapes. The
// set is exactly what clearance reaches for; review threads are not here.
type SCMRepository interface {
	HeadSHA(ctx context.Context, number int) (string, error)
	PullRequestTitle(ctx context.Context, number int) (string, error)
	ChangedPaths(ctx context.Context, number int) ([]string, error)
	CanWrite(ctx context.Context, user string) (bool, error)
	ReferralStatus(ctx context.Context, sha string) (*clearance.Status, error)
	PostStatus(ctx context.Context, s Status) error
	CreateComment(ctx context.Context, number int, body string) error
}

var _ SCMRepository = (*GitHubRepository)(nil)

// GitHubRepository is an SCMRepository over a Client, bound to one Repo. Each
// method is the Client method of the same name, unchanged.
type GitHubRepository struct {
	Client *Client
	Repo   Repo
}

// HeadSHA resolves pull request number's current head.
func (g *GitHubRepository) HeadSHA(ctx context.Context, number int) (string, error) {
	return g.Client.HeadSHA(ctx, g.Repo, number)
}

// PullRequestTitle resolves pull request number's current title.
func (g *GitHubRepository) PullRequestTitle(ctx context.Context, number int) (string, error) {
	return g.Client.PullRequestTitle(ctx, g.Repo, number)
}

// ChangedPaths lists every path pull request number touches.
func (g *GitHubRepository) ChangedPaths(ctx context.Context, number int) ([]string, error) {
	return g.Client.ChangedPaths(ctx, g.Repo, number)
}

// CanWrite reports whether user may push to the repository.
func (g *GitHubRepository) CanWrite(ctx context.Context, user string) (bool, error) {
	return g.Client.CanWrite(ctx, g.Repo, user)
}

// ReferralStatus returns the referral status standing on sha, or nil when
// none is.
func (g *GitHubRepository) ReferralStatus(ctx context.Context, sha string) (*clearance.Status, error) {
	return g.Client.ReferralStatus(ctx, g.Repo, sha)
}

// PostStatus records s on the revision it names.
func (g *GitHubRepository) PostStatus(ctx context.Context, s Status) error {
	return g.Client.PostStatus(ctx, g.Repo, s)
}

// CreateComment posts body as a new comment on pull request number.
func (g *GitHubRepository) CreateComment(ctx context.Context, number int, body string) error {
	return g.Client.CreateComment(ctx, g.Repo, number, body)
}

// repositoryKey is unexported so the only Write that can store an
// SCMRepository on a Context is the one InitializeSCM's Run returns.
var repositoryKey = flow.NewKey[SCMRepository]("scm-repository")

// Get reads the SCMRepository InitializeSCM joined into r, reporting false
// when no stage has run it.
func Get(r flow.Reader) (SCMRepository, bool) {
	return flow.Get(r, repositoryKey)
}

// errNoToken is the refusal a run without a write-capable credential gets.
// Clearance posts statuses and comments, so there is nothing it can do
// read-only.
var errNoToken = errors.New("clearance needs GITHUB_TOKEN with `statuses: write` and `pull-requests: write`")

// InitializeSCM is the StageComponent that joins a GitHubRepository for Repo
// into the Flow's Context. It runs after trust.InitializeTrustContext, whose
// TrustedContext it reads.
type InitializeSCM struct {
	Repo Repo
}

// Name is the component's name in a Flow's reports.
func (InitializeSCM) Name() string {
	return "initialize-scm"
}

// Run refuses a run the TrustedContext says holds no credential, and
// otherwise builds the client from the token in the environment.
//
// TrustedContext answers only whether a credential exists, never what it is,
// so the token is read here from the same variables trust decides from. A
// missing TrustedContext is treated as no credential, and an environment that
// no longer carries a token is refused the same way rather than building a
// client that would send unauthenticated requests.
func (i InitializeSCM) Run(_ context.Context, in flow.View) (flow.Result, error) {
	result := flow.Result{Policy: flow.FailFlow}
	if trusted, ok := trust.Get(in); !ok || !trusted.CanWrite() {
		return result, errNoToken
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		return result, errNoToken
	}
	var repository SCMRepository = &GitHubRepository{Client: New(token), Repo: i.Repo}
	result.Writes = []flow.Write{flow.Put(repositoryKey, repository)}
	return result, nil
}

