package forge

import (
	"context"
	"fmt"

	"lydite/lydite/internal/clearance"
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
	IssueComment(ctx context.Context, id int64) (IssueComment, error)
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

// NewGitHubRepository binds a Client holding t's credential to the repository
// t names.
//
// Both come from t and from nothing else, so the repository every call is
// made against is the one the run was started in, never one a payload or a
// flag named. A TrustedContext naming no repository — the zero value — is an
// error. One holding no credential is not: it builds a client that sends
// unauthenticated requests, and whether a run may write at all is
// t.CanWrite's answer, asked by whoever holds t.
func NewGitHubRepository(t trust.TrustedContext) (*GitHubRepository, error) {
	repo, err := ParseRepo(t.Repository())
	if err != nil {
		return nil, fmt.Errorf("the trusted repository: %w", err)
	}
	return &GitHubRepository{Client: New(t.Token()), Repo: repo}, nil
}

// IssueComment resolves comment id, and the issue or pull request it is on.
func (g *GitHubRepository) IssueComment(ctx context.Context, id int64) (IssueComment, error) {
	return g.Client.IssueComment(ctx, g.Repo, id)
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
