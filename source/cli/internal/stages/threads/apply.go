package threadsstages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/threads"
)

// TakeDownIn is the plan whose deletes TakeDown performs.
type TakeDownIn struct {
	Repository forge.SCMRepository
	// Ref is the pull request the deletes are on; only Ref.Number is used.
	Ref forge.PullRequestRef
	// Ops is the whole plan; TakeDown reads Ops.Delete.
	Ops threads.Ops
}

// TakeDownOut is what the deletes came to.
type TakeDownOut struct {
	// Refused is how many deletes the platform refused and were answered
	// instead, which is len(Answered).
	Refused int
	// Answered is the id of every comment answered instead of deleted, in
	// the order the plan named them.
	Answered []int64
}

// TakeDownError is TakeDown's failure, carrying the comments already answered
// before it: a failed stage's output is unavailable, so the progress it made
// travels in its error.
type TakeDownError struct {
	// Answered is the id of every comment answered instead of deleted
	// before the failure, in order.
	Answered []int64
	Err      error
}

func (e *TakeDownError) Error() string { return e.Err.Error() }

func (e *TakeDownError) Unwrap() error { return e.Err }

// TakeDown removes every comment the plan deletes.
//
// A refused delete is not a failure. The platform will not let one identity
// delete another's comment, so the thread is left standing with the reply the
// plan already carries for that case — the same path a thread somebody else
// spoke in takes. Any other delete error, or a reply that fails other than as
// unfound, is a *TakeDownError.
func TakeDown(ctx context.Context, in TakeDownIn) (TakeDownOut, error) {
	var answered []int64
	for _, del := range in.Ops.Delete {
		instead, err := takeDown(ctx, in.Repository, in.Ref.Number, del)
		if err != nil {
			return TakeDownOut{}, &TakeDownError{Answered: answered, Err: err}
		}
		if instead {
			answered = append(answered, del.Comment)
		}
	}
	return TakeDownOut{Refused: len(answered), Answered: answered}, nil
}

// takeDown removes one comment, and reports whether it had to be answered
// instead.
//
// The platform refuses a delete of another identity's comment with a 403 when
// this one can see the comment and a 404 when it cannot, so both take the
// answering path. A reply that is itself unfound settles which of the two a
// 404 was: the comment is gone, the state the delete was asking for, and
// there is nothing left to say to it.
func takeDown(ctx context.Context, repository forge.SCMRepository, number int, del threads.Delete) (bool, error) {
	err := repository.DeleteReviewComment(ctx, del.Comment)
	switch {
	case err == nil:
		return false, nil
	case !forge.Forbidden(err) && !forge.NotFound(err):
		return false, err
	case del.Refused == "":
		// A refusal is refused for the whole thread at once, and only one
		// operation in it carries what to say. The rest are left as they
		// are rather than answered again.
		return false, nil
	}
	replyErr := repository.ReplyToReviewComment(ctx, number, del.Comment, del.Refused)
	switch {
	case replyErr == nil:
		return true, nil
	case forge.NotFound(replyErr):
		return false, nil
	default:
		return false, replyErr
	}
}

// AnswerIn is the plan whose replies Answer posts.
type AnswerIn struct {
	Repository forge.SCMRepository
	// Ref is the pull request the replies are on; only Ref.Number is used.
	Ref forge.PullRequestRef
	// Ops is the whole plan; Answer reads Ops.Reply.
	Ops threads.Ops
}

// Answer replies to every thread the plan leaves standing with something to
// say, and stops at the first reply that fails.
func Answer(ctx context.Context, in AnswerIn) (struct{}, error) {
	for _, reply := range in.Ops.Reply {
		if err := in.Repository.ReplyToReviewComment(ctx, in.Ref.Number, reply.Comment, reply.Body); err != nil {
			return struct{}{}, err
		}
	}
	return struct{}{}, nil
}

// OpenIn is the plan whose new threads Open posts.
type OpenIn struct {
	Repository forge.SCMRepository
	// Ref is the pull request the threads are opened on; only Ref.Number is
	// used, since the head they are anchored to is Ops.Head.
	Ref forge.PullRequestRef
	// Ops is the whole plan; Open reads Ops.Create and Ops.Head.
	Ops threads.Ops
}

// OpenOut is what Open posted.
type OpenOut struct {
	// Posted is how many new threads reached the pull request.
	Posted int
}

// OpenError is Open's failure, carrying how many claims landed and how many
// did not: a failed stage's output is unavailable, so the progress it made
// travels in its error.
type OpenError struct {
	// Lost is how many located findings reached no surface.
	Lost int
	// Posted is how many reached the pull request before the failure.
	Posted int
	Err    error
}

func (e *OpenError) Error() string {
	return fmt.Sprintf("%d located finding(s) reached no surface: %v", e.Lost, e.Err)
}

func (e *OpenError) Unwrap() error { return e.Err }

// Open posts the new threads: the line-anchored ones in one review, and the
// file-anchored ones one at a time.
//
// The split is the platform's. A review's comments are drafts with no
// `subjectType` field and a required position, so a claim about a whole file
// is refused inside one and accepted on its own — which makes a run one review
// plus a call per file-level thread. The review is handed every create and
// leaves the file-anchored ones out itself.
//
// A review the platform refuses, or a file thread refused after others
// landed, is an *OpenError naming how many claims reached no surface. Never a
// silent green — the claims are still in the terminal, the job log and the
// uploaded report directory, and a run that quietly posted nothing is
// indistinguishable from a change with nothing wrong with it. The count is
// what was posted rather than what was asked for: a review that landed and
// one file thread the platform then refused has lost one claim, not all of
// them.
func Open(ctx context.Context, in OpenIn) (OpenOut, error) {
	creates := in.Ops.Create
	var onLines int
	for _, create := range creates {
		if create.Subject != "file" {
			onLines++
		}
	}
	lost := func(posted int, err error) (OpenOut, error) {
		return OpenOut{}, &OpenError{Lost: len(creates) - posted, Posted: posted, Err: err}
	}
	if err := in.Repository.CreateReview(ctx, in.Ref.Number, in.Ops.Head, creates); err != nil {
		return lost(0, err)
	}
	posted := onLines
	for _, create := range creates {
		if create.Subject != "file" {
			continue
		}
		if err := in.Repository.CreateFileComment(ctx, in.Ref.Number, in.Ops.Head, create); err != nil {
			return lost(posted, err)
		}
		posted++
	}
	return OpenOut{Posted: posted}, nil
}
