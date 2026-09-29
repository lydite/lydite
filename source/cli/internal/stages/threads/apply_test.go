package threadsstages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/threads"
)

var (
	forbidden = &forge.APIError{Status: http.StatusForbidden, Body: "Must have admin rights"}
	notFound  = &forge.APIError{Status: http.StatusNotFound, Body: "Not Found"}
	serverErr = &forge.APIError{Status: http.StatusInternalServerError, Body: "boom"}
)

// recorder is a threadsFakeRepository that records every write in the order
// it was made, answering each from the errors it was given by call.
type recorder struct {
	calls []string
}

func (r *recorder) repository(t *testing.T, errs map[string]error) *threadsFakeRepository {
	note := func(call string) error {
		r.calls = append(r.calls, call)
		return errs[call]
	}
	return &threadsFakeRepository{t: t,
		deleteReviewComment: func(_ context.Context, id int64) error {
			return note(fmt.Sprintf("delete %d", id))
		},
		replyToReviewComment: func(_ context.Context, number int, id int64, body string) error {
			return note(fmt.Sprintf("reply %d#%d %s", number, id, body))
		},
		createReview: func(_ context.Context, number int, head string, comments []threads.Create) error {
			return note(fmt.Sprintf("review %d@%s %d", number, head, len(comments)))
		},
		createFileComment: func(_ context.Context, number int, head string, create threads.Create) error {
			return note(fmt.Sprintf("file %d@%s %s", number, head, create.Path))
		},
	}
}

func TestADeleteRefusedWith403Or404IsAnsweredOncePerThread(t *testing.T) {
	var r recorder
	repository := r.repository(t, map[string]error{
		"delete 1": forbidden,
		"delete 2": forbidden,
		"delete 3": notFound,
	})
	ops := threads.Ops{Delete: []threads.Delete{
		{Comment: 1, Refused: "cannot delete"},
		// The same thread's second comment carries nothing to say.
		{Comment: 2},
		{Comment: 3, Refused: "cannot see"},
		{Comment: 4},
	}}

	out, err := TakeDown(context.Background(), TakeDownIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	if err != nil {
		t.Fatalf("a refused delete is not a failure: %v", err)
	}
	want := []string{"delete 1", "reply 7#1 cannot delete", "delete 2", "delete 3", "reply 7#3 cannot see", "delete 4"}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
	if !reflect.DeepEqual(out.Answered, []int64{1, 3}) || out.Refused != 2 {
		t.Errorf("Answered = %v, Refused = %d; want [1 3] and 2", out.Answered, out.Refused)
	}
}

func TestAnAnswerToACommentAlreadyGoneIsNotAFailure(t *testing.T) {
	var r recorder
	repository := r.repository(t, map[string]error{
		"delete 1":                notFound,
		"reply 7#1 cannot delete": notFound,
	})
	ops := threads.Ops{Delete: []threads.Delete{{Comment: 1, Refused: "cannot delete"}}}

	out, err := TakeDown(context.Background(), TakeDownIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	if err != nil {
		t.Fatalf("a comment that is gone is the state the delete asked for: %v", err)
	}
	if len(out.Answered) != 0 || out.Refused != 0 {
		t.Errorf("Answered = %v, Refused = %d; a comment that is gone was not answered", out.Answered, out.Refused)
	}
}

func TestADeleteErrorThatIsNotARefusalCarriesTheEarlierAnswers(t *testing.T) {
	var r recorder
	repository := r.repository(t, map[string]error{
		"delete 1": forbidden,
		"delete 2": serverErr,
	})
	ops := threads.Ops{Delete: []threads.Delete{
		{Comment: 1, Refused: "cannot delete"},
		{Comment: 2, Refused: "cannot delete"},
		{Comment: 3},
	}}

	_, err := TakeDown(context.Background(), TakeDownIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	var failed *TakeDownError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want a *TakeDownError", err)
	}
	if !reflect.DeepEqual(failed.Answered, []int64{1}) {
		t.Errorf("Answered = %v, want [1]", failed.Answered)
	}
	if !errors.Is(err, serverErr) {
		t.Errorf("errors.Is does not reach the delete's own error through %v", err)
	}
	if err.Error() != serverErr.Error() {
		t.Errorf("Error() = %q, want the delete's own text %q", err.Error(), serverErr.Error())
	}
	if last := r.calls[len(r.calls)-1]; last != "delete 2" {
		t.Errorf("wrote %q after the failure, want nothing past delete 2", last)
	}
}

func TestAnAnswerThatFailsOtherThanUnfoundIsATakeDownError(t *testing.T) {
	var r recorder
	repository := r.repository(t, map[string]error{
		"delete 1":                forbidden,
		"reply 7#1 cannot delete": serverErr,
	})
	ops := threads.Ops{Delete: []threads.Delete{{Comment: 1, Refused: "cannot delete"}}}

	_, err := TakeDown(context.Background(), TakeDownIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	var failed *TakeDownError
	if !errors.As(err, &failed) || !errors.Is(err, serverErr) {
		t.Fatalf("err = %v, want a *TakeDownError wrapping the reply's error", err)
	}
	if len(failed.Answered) != 0 {
		t.Errorf("Answered = %v, want none: the one answer failed", failed.Answered)
	}
}

func TestAnswerRepliesInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	var r recorder
	repository := r.repository(t, map[string]error{"reply 7#2 second": serverErr})
	ops := threads.Ops{Reply: []threads.Reply{
		{Comment: 1, Body: "first"},
		{Comment: 2, Body: "second"},
		{Comment: 3, Body: "third"},
	}}

	_, err := Answer(context.Background(), AnswerIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	if !errors.Is(err, serverErr) {
		t.Fatalf("err = %v, want the reply's own error", err)
	}
	if want := []string{"reply 7#1 first", "reply 7#2 second"}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func creates() []threads.Create {
	return []threads.Create{
		{Fingerprint: "a", Path: "a.go", Line: 1, Subject: "line"},
		{Fingerprint: "b", Path: "b.go", Subject: "file"},
		{Fingerprint: "c", Path: "c.go", Line: 2, Subject: "line"},
		{Fingerprint: "d", Path: "d.go", Subject: "file"},
	}
}

func TestOpenHandsTheReviewEveryCreateThenPostsEachFileComment(t *testing.T) {
	var r recorder
	repository := r.repository(t, nil)
	ops := threads.Ops{Head: "abc123", Create: creates()}

	out, err := Open(context.Background(), OpenIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"review 7@abc123 4", "file 7@abc123 b.go", "file 7@abc123 d.go"}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
	if out.Posted != 4 {
		t.Errorf("Posted = %d, want 4", out.Posted)
	}
}

func TestARefusedReviewLosesEveryClaim(t *testing.T) {
	var r recorder
	refused := &forge.APIError{Status: http.StatusUnprocessableEntity, Body: "Validation Failed"}
	repository := r.repository(t, map[string]error{"review 7@abc123 4": refused})
	ops := threads.Ops{Head: "abc123", Create: creates()}

	_, err := Open(context.Background(), OpenIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	var failed *OpenError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want an *OpenError", err)
	}
	if failed.Lost != 4 || failed.Posted != 0 {
		t.Errorf("Lost = %d, Posted = %d; want 4 and 0", failed.Lost, failed.Posted)
	}
	if !errors.Is(err, refused) {
		t.Errorf("errors.Is does not reach the review's own error through %v", err)
	}
	if want := "4 located finding(s) reached no surface: " + refused.Error(); err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if len(r.calls) != 1 {
		t.Errorf("calls = %q, want no file comment after a refused review", r.calls)
	}
}

func TestARefusedFileCommentLosesOnlyWhatDidNotLand(t *testing.T) {
	var r recorder
	repository := r.repository(t, map[string]error{"file 7@abc123 d.go": serverErr})
	ops := threads.Ops{Head: "abc123", Create: creates()}

	_, err := Open(context.Background(), OpenIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops})
	var failed *OpenError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want an *OpenError", err)
	}
	if failed.Lost != 1 || failed.Posted != 3 {
		t.Errorf("Lost = %d, Posted = %d; want 1 and 3: the review and one file thread landed", failed.Lost, failed.Posted)
	}
	if !errors.Is(err, serverErr) {
		t.Errorf("errors.Is does not reach the file comment's own error through %v", err)
	}
	if want := "1 located finding(s) reached no surface: " + serverErr.Error(); err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestApplyingWritesDeletesThenRepliesThenTheReviewThenFileComments(t *testing.T) {
	var r recorder
	repository := r.repository(t, map[string]error{"delete 1": forbidden})
	ops := threads.Ops{
		Head:   "abc123",
		Create: []threads.Create{{Path: "a.go", Line: 1, Subject: "line"}, {Path: "b.go", Subject: "file"}},
		Reply:  []threads.Reply{{Comment: 5, Body: "fixed"}},
		Delete: []threads.Delete{{Comment: 1, Refused: "cannot delete"}, {Comment: 2}},
	}
	ctx := context.Background()

	if _, err := TakeDown(ctx, TakeDownIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops}); err != nil {
		t.Fatal(err)
	}
	if _, err := Answer(ctx, AnswerIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, OpenIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}, Ops: ops}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"delete 1", "reply 7#1 cannot delete", "delete 2",
		"reply 7#5 fixed",
		"review 7@abc123 2",
		"file 7@abc123 b.go",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}
