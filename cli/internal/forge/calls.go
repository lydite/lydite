package forge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lydite/lydite/internal/clearance"
)

// HeadSHA resolves a pull request's current head.
//
// A comment event names an issue, never a revision, so this is the step that
// turns "someone commented on pull request 40" into the commit a decision
// can be about. Resolving it explicitly rather than trusting anything in the
// comment is the whole reason a clearance can name a revision at all.
func (c *Client) HeadSHA(ctx context.Context, repo Repo, number int) (string, error) {
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", escape(repo.Owner), escape(repo.Name), number)
	if err := c.do(ctx, "GET", path, nil, &pr); err != nil {
		return "", fmt.Errorf("resolving the head of %s#%d: %w", repo, number, err)
	}
	if pr.Head.SHA == "" {
		return "", fmt.Errorf("%s#%d reports no head revision", repo, number)
	}
	return pr.Head.SHA, nil
}

// CanWrite reports whether a user may push to the repository.
//
// This is read about the commenter rather than taken from the comment, which
// is what makes it usable as a floor: nothing an author writes can produce
// it. It is not the whole of the trust — whoever holds the repository's
// credentials satisfies it — and the authenticator code in #25 is what
// closes that.
func (c *Client) CanWrite(ctx context.Context, repo Repo, user string) (bool, error) {
	var perm struct {
		Permission string `json:"permission"`
	}
	path := fmt.Sprintf("/repos/%s/%s/collaborators/%s/permission",
		escape(repo.Owner), escape(repo.Name), escape(user))
	if err := c.do(ctx, "GET", path, nil, &perm); err != nil {
		// A user who is not a collaborator at all answers 404, which is
		// the commonest case on a public repository and is an answer
		// rather than a failure.
		if NotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s's permission on %s: %w", user, repo, err)
	}
	return perm.Permission == "admin" || perm.Permission == "write" || perm.Permission == "maintain", nil
}

// ReferralStatus returns the referral status standing on a revision, or nil
// when none is.
//
// The platform returns statuses newest first, so the first match under the
// context is the standing one. Older entries under the same context are the
// history of that revision's verdict and are deliberately not merged into
// the answer: what is in force is one state, and reading further back would
// find a pending entry underneath every clearance.
func (c *Client) ReferralStatus(ctx context.Context, repo Repo, sha string) (*clearance.Status, error) {
	var statuses []struct {
		State       string    `json:"state"`
		Context     string    `json:"context"`
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/statuses?per_page=100",
		escape(repo.Owner), escape(repo.Name), escape(sha))
	if err := c.do(ctx, "GET", path, nil, &statuses); err != nil {
		if NotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the statuses on %s: %w", short(sha), err)
	}
	for _, s := range statuses {
		if s.Context != clearance.Context {
			continue
		}
		return &clearance.Status{
			State:       clearance.State(s.State),
			Description: s.Description,
			CreatedAt:   s.CreatedAt,
		}, nil
	}
	return nil, nil
}

// PublishStatus records a verdict on a revision.
//
// The description is what a reader sees beside a yellow dot that otherwise
// looks like a job still running, so it names what is being waited for
// rather than restating the state.
func (c *Client) PublishStatus(ctx context.Context, repo Repo, sha string, state clearance.State, description, targetURL string) error {
	body := map[string]string{
		"state":       string(state),
		"context":     clearance.Context,
		"description": truncate(description, 140),
	}
	if targetURL != "" {
		body["target_url"] = targetURL
	}
	path := fmt.Sprintf("/repos/%s/%s/statuses/%s", escape(repo.Owner), escape(repo.Name), escape(sha))
	if err := c.do(ctx, "POST", path, body, nil); err != nil {
		return fmt.Errorf("publishing %s on %s: %w", state, short(sha), err)
	}
	return nil
}

// Comment is one comment on a pull request's conversation.
type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// FindComment returns the first comment containing marker, or nil.
//
// The marker is an HTML comment in the body rather than a match on the
// author, because the author is whoever's token the workflow runs under and
// that is not lydite's to rely on. Matching on our own text also means a
// person editing the comment's prose does not detach it.
func (c *Client) FindComment(ctx context.Context, repo Repo, number int, marker string) (*Comment, error) {
	// A busy pull request holds more comments than one page, and the
	// sticky one is the oldest lydite wrote — so the walk has to reach the
	// end rather than stopping at the first page.
	for page := 1; page <= 10; page++ {
		var comments []Comment
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d",
			escape(repo.Owner), escape(repo.Name), number, page)
		if err := c.do(ctx, "GET", path, nil, &comments); err != nil {
			return nil, fmt.Errorf("reading comments on %s#%d: %w", repo, number, err)
		}
		for _, comment := range comments {
			if strings.Contains(comment.Body, marker) {
				return &comment, nil
			}
		}
		if len(comments) < 100 {
			break
		}
	}
	return nil, nil
}

// CreateComment adds a comment to a pull request's conversation.
func (c *Client) CreateComment(ctx context.Context, repo Repo, number int, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", escape(repo.Owner), escape(repo.Name), number)
	if err := c.do(ctx, "POST", path, map[string]string{"body": body}, nil); err != nil {
		return fmt.Errorf("commenting on %s#%d: %w", repo, number, err)
	}
	return nil
}

// UpdateComment replaces a comment's body.
func (c *Client) UpdateComment(ctx context.Context, repo Repo, id int64, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d", escape(repo.Owner), escape(repo.Name), id)
	if err := c.do(ctx, "PATCH", path, map[string]string{"body": body}, nil); err != nil {
		return fmt.Errorf("updating comment %d on %s: %w", id, repo, err)
	}
	return nil
}

// UpsertComment keeps exactly one comment carrying marker on a pull request.
//
// One comment edited in place rather than one per push: a bot that appends a
// verdict to every push buries the conversation it is meant to inform, and
// the standing verdict is the only one that is true.
func (c *Client) UpsertComment(ctx context.Context, repo Repo, number int, marker, body string) error {
	existing, err := c.FindComment(ctx, repo, number, marker)
	if err != nil {
		return err
	}
	if existing == nil {
		return c.CreateComment(ctx, repo, number, body)
	}
	return c.UpdateComment(ctx, repo, existing.ID, body)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// truncate keeps a description inside the platform's limit, which counts
// characters rather than bytes.
//
// A description cut off mid-word still says which verdict is in force, which
// is the part that matters; being rejected for length says nothing at all.
// Cutting on a rune boundary matters because a path or a rule name can carry
// anything the source does, and half a rune renders as a replacement
// character in the one line a reader gets.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
