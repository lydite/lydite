package threadsstages

import (
	"context"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/threads"
)

// threadsFakeRepository is a forge.SCMRepository whose every method fails the
// test if called without a function supplied for it — a stage calling a
// method nothing in the test case expects is itself the bug under test.
type threadsFakeRepository struct {
	t *testing.T

	reviewComments       func(ctx context.Context, number int) ([]threads.Comment, error)
	createReview         func(ctx context.Context, number int, head string, comments []threads.Create) error
	createFileComment    func(ctx context.Context, number int, head string, create threads.Create) error
	replyToReviewComment func(ctx context.Context, number int, id int64, body string) error
	deleteReviewComment  func(ctx context.Context, id int64) error
}

func (f *threadsFakeRepository) IssueComment(_ context.Context, _ int64) (forge.IssueComment, error) {
	f.t.Fatal("IssueComment: unexpected call")
	return forge.IssueComment{}, nil
}

func (f *threadsFakeRepository) HeadSHA(_ context.Context, _ int) (string, error) {
	f.t.Fatal("HeadSHA: unexpected call")
	return "", nil
}

func (f *threadsFakeRepository) PullRequestTitle(_ context.Context, _ int) (string, error) {
	f.t.Fatal("PullRequestTitle: unexpected call")
	return "", nil
}

func (f *threadsFakeRepository) ChangedPaths(_ context.Context, _ int) ([]string, error) {
	f.t.Fatal("ChangedPaths: unexpected call")
	return nil, nil
}

func (f *threadsFakeRepository) CanWrite(_ context.Context, _ string) (bool, error) {
	f.t.Fatal("CanWrite: unexpected call")
	return false, nil
}

func (f *threadsFakeRepository) ReferralStatus(_ context.Context, _ string) (*clearance.Status, error) {
	f.t.Fatal("ReferralStatus: unexpected call")
	return nil, nil
}

func (f *threadsFakeRepository) PostStatus(_ context.Context, _ forge.Status) error {
	f.t.Fatal("PostStatus: unexpected call")
	return nil
}

func (f *threadsFakeRepository) CreateComment(_ context.Context, _ int, _ string) error {
	f.t.Fatal("CreateComment: unexpected call")
	return nil
}

func (f *threadsFakeRepository) ReviewComments(ctx context.Context, number int) ([]threads.Comment, error) {
	if f.reviewComments == nil {
		f.t.Fatal("ReviewComments: unexpected call")
	}
	return f.reviewComments(ctx, number)
}

func (f *threadsFakeRepository) CreateReview(ctx context.Context, number int, head string, comments []threads.Create) error {
	if f.createReview == nil {
		f.t.Fatal("CreateReview: unexpected call")
	}
	return f.createReview(ctx, number, head, comments)
}

func (f *threadsFakeRepository) CreateFileComment(ctx context.Context, number int, head string, create threads.Create) error {
	if f.createFileComment == nil {
		f.t.Fatal("CreateFileComment: unexpected call")
	}
	return f.createFileComment(ctx, number, head, create)
}

func (f *threadsFakeRepository) ReplyToReviewComment(ctx context.Context, number int, id int64, body string) error {
	if f.replyToReviewComment == nil {
		f.t.Fatal("ReplyToReviewComment: unexpected call")
	}
	return f.replyToReviewComment(ctx, number, id, body)
}

func (f *threadsFakeRepository) DeleteReviewComment(ctx context.Context, id int64) error {
	if f.deleteReviewComment == nil {
		f.t.Fatal("DeleteReviewComment: unexpected call")
	}
	return f.deleteReviewComment(ctx, id)
}

// fakeReader is a FindingsReader answering from its maps: a directory in err
// fails with that error, and any other answers with what found holds for it.
type fakeReader struct {
	found map[string][]finding.Finding
	err   map[string]error
}

func (r fakeReader) Findings(dir string) ([]finding.Finding, error) {
	if err, ok := r.err[dir]; ok {
		return nil, err
	}
	return r.found[dir], nil
}
