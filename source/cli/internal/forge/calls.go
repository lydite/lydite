package forge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/threads"
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

// ReviewComment is one comment on a pull request's diff, as the platform
// reports it.
//
// Position and Line are two different questions and both are asked. Line is
// where the comment sits in the current diff; position comes back null when
// the change has moved out from under the comment, which is the only way to
// learn that the platform has collapsed a thread behind its "show outdated"
// toggle — a thread that blocks a merge and that the author cannot see.
//
// SubjectType is what keeps that reading honest. A comment on a whole file has
// no position by construction rather than by the change having moved, so the
// null alone would report every file-level thread as outdated and take it down
// and repost it on every push.
type reviewComment struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	Path        string `json:"path"`
	Line        *int   `json:"line"`
	Position    *int   `json:"position"`
	SubjectType string `json:"subject_type"`
	InReplyTo   int64  `json:"in_reply_to_id"`
}

// ReviewComments lists every comment on a pull request's diff, replies
// included.
//
// One listing rather than a query per finding: the delta needs the whole set
// to decide what has no thread yet, and asking per fingerprint would be one
// request per claim to learn the same thing.
//
// The walk is capped the way the conversation's is. A pull request with more
// review comments than this has a thread lydite cannot see, and the delta
// treats what it cannot see as absent — which reposts a claim rather than
// deleting somebody's thread, the safe direction of the two.
func (c *Client) ReviewComments(ctx context.Context, repo Repo, number int) ([]threads.Comment, error) {
	var out []threads.Comment
	for page := range reviewPages {
		var comments []reviewComment
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=%d&page=%d",
			escape(repo.Owner), escape(repo.Name), number, reviewPerPage, page+1)
		if err := c.do(ctx, "GET", path, nil, &comments); err != nil {
			return nil, fmt.Errorf("reading the review comments on %s#%d: %w", repo, number, err)
		}
		for _, rc := range comments {
			out = append(out, threads.Comment{
				ID: rc.ID, Body: rc.Body, Path: rc.Path,
				Line:      derefOr(rc.Line, 0),
				Outdated:  rc.Position == nil && rc.SubjectType != "file",
				InReplyTo: rc.InReplyTo,
			})
		}
		if len(comments) < reviewPerPage {
			break
		}
	}
	return out, nil
}

// reviewPages and reviewPerPage bound the walk. They are a `range` and a page
// size rather than a loop condition and an increment, so there is no boundary
// to shift and no step to drop: the walk visits exactly this many pages or
// stops at the first short one.
const (
	reviewPages   = 10
	reviewPerPage = 100
)

// CreateReview opens every line-anchored thread in one review.
//
// One review and not one comment each, because a review notifies once however
// many lines it touches, and a run that opens twelve threads individually
// sends twelve emails about one push. The event is COMMENT and never
// REQUEST_CHANGES or APPROVE: review approval is a different mechanism with
// different rules about who may give one, and lydite must not touch it.
//
// The review carries no body of its own. A summary line above the threads
// would be a second standing verdict beside the comment that already carries
// one, reposted on every push.
//
// A comment must anchor inside the diff's own hunks — the API answers 422 for
// a line outside them, where the web UI would have let a human comment. That
// is why a finding's anchor is decided by the gate that made it, against the
// changed-line map: those lines are a strict subset of what the platform
// accepts, so "lydite says anchorable" implies "the platform takes it" and
// never the reverse.
//
// **A file-anchored claim cannot travel here.** A review's comments are
// `DraftPullRequestReviewComment`, which has no `subjectType` field and
// requires a position — the API answers 422 for both. So CreateFileComment
// opens those one at a time, and a run is one review plus a call per thread
// that is about a file rather than a line.
func (c *Client) CreateReview(ctx context.Context, repo Repo, number int, head string, comments []threads.Create) error {
	type reviewInput struct {
		Path string `json:"path"`
		Body string `json:"body"`
		Line int    `json:"line"`
	}
	inputs := make([]reviewInput, 0, len(comments))
	for _, create := range comments {
		if create.Subject == "file" {
			continue
		}
		inputs = append(inputs, reviewInput{Path: create.Path, Body: create.Body, Line: create.Line})
	}
	if len(inputs) == 0 {
		return nil
	}
	body := map[string]any{"event": "COMMENT", "comments": inputs}
	if head != "" {
		body["commit_id"] = head
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", escape(repo.Owner), escape(repo.Name), number)
	if err := c.do(ctx, "POST", path, body, nil); err != nil {
		return fmt.Errorf("opening %d thread(s) on %s#%d: %w", len(inputs), repo, number, err)
	}
	return nil
}

// CreateFileComment opens a thread on a whole file.
//
// `subject_type: file` is accepted here and refused inside a review, which is
// why this exists separately. It needs the revision explicitly: a comment with
// no line has nothing else that places it.
func (c *Client) CreateFileComment(ctx context.Context, repo Repo, number int, head string, create threads.Create) error {
	body := map[string]any{"path": create.Path, "body": create.Body, "subject_type": "file"}
	if head != "" {
		body["commit_id"] = head
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", escape(repo.Owner), escape(repo.Name), number)
	if err := c.do(ctx, "POST", path, body, nil); err != nil {
		return fmt.Errorf("opening a thread on %s in %s#%d: %w", create.Path, repo, number, err)
	}
	return nil
}

// ReplyToReviewComment adds to a thread rather than opening one.
//
// It is outside the review, because a review's comments[] can only open
// threads. A run is therefore one review plus a call per thread it leaves
// standing.
func (c *Client) ReplyToReviewComment(ctx context.Context, repo Repo, number int, id int64, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments/%d/replies",
		escape(repo.Owner), escape(repo.Name), number, id)
	if err := c.do(ctx, "POST", path, map[string]string{"body": body}, nil); err != nil {
		return fmt.Errorf("replying to review comment %d on %s#%d: %w", id, repo, number, err)
	}
	return nil
}

// DeleteReviewComment removes one comment lydite wrote.
//
// An identity may only delete what it authored, so this is refused for a
// thread the other identity opened — the handover between lydite's app and a
// consumer's own bot. The refusal is a 403 where the identity can see the
// comment and a 404 where it cannot, which is why the caller treats both the
// same way: answer it by replying, and read a reply that is also unfound as
// the comment simply being gone.
func (c *Client) DeleteReviewComment(ctx context.Context, repo Repo, id int64) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/comments/%d", escape(repo.Owner), escape(repo.Name), id)
	if err := c.do(ctx, "DELETE", path, nil, nil); err != nil {
		return fmt.Errorf("deleting review comment %d on %s: %w", id, repo, err)
	}
	return nil
}

func derefOr(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}
