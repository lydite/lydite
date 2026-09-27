package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CommentEvent is the part of an issue_comment payload a decision is made
// from.
//
// The payload is read whole rather than assembled from flags a driver chose
// to forward. A workflow writes exactly this document to the path in
// GITHUB_EVENT_PATH, and it is the same document the platform delivers to an
// app, so the deciding side does not learn which one invoked it.
type CommentEvent struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
		// PullRequest is present only on a comment on a pull request.
		// An issue comment names no revision and there is nothing to
		// decide about it.
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// LoadCommentEvent reads a webhook payload from disk.
func LoadCommentEvent(path string) (CommentEvent, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the platform's own GITHUB_EVENT_PATH or the --event flag, supplied by whoever runs lydite, not untrusted remote input
	if err != nil {
		return CommentEvent{}, fmt.Errorf("reading the event payload: %w", err)
	}
	var event CommentEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return CommentEvent{}, fmt.Errorf("parsing the event payload: %w", err)
	}
	return event, nil
}

// OnPullRequest reports whether the comment is on a pull request.
func (e CommentEvent) OnPullRequest() bool { return e.Issue.PullRequest != nil }

// CommentRef is what an issue_comment payload points at: the comment's id,
// and the repository the payload claims it is in.
//
// It is a pointer and not a description. The body, the author, the time and
// the thread are the payload's copy of a comment that can have been edited or
// deleted since, so they are resolved live through Client.IssueComment from
// the id alone. Repository is the payload's claim and is not trusted either:
// it is read only so a caller can compare it against the repository the run
// was started in and refuse a payload naming any other.
type CommentRef struct {
	ID         int64
	Repository string
}

// ReadCommentRef reads a CommentRef out of an issue_comment payload on disk,
// and deliberately nothing else. A payload naming no comment, or no
// repository, is refused: there is nothing to resolve, or nothing to check the
// claim against.
func ReadCommentRef(path string) (CommentRef, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the platform's own GITHUB_EVENT_PATH or the --event flag, supplied by whoever runs lydite, not untrusted remote input
	if err != nil {
		return CommentRef{}, fmt.Errorf("reading the event payload: %w", err)
	}
	var event struct {
		Comment struct {
			ID int64 `json:"id"`
		} `json:"comment"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return CommentRef{}, fmt.Errorf("parsing the event payload: %w", err)
	}
	if event.Comment.ID <= 0 {
		return CommentRef{}, errors.New("the event payload names no comment")
	}
	if event.Repository.FullName == "" {
		return CommentRef{}, errors.New("the event payload names no repository")
	}
	return CommentRef{ID: event.Comment.ID, Repository: event.Repository.FullName}, nil
}

// PullRequestEvent is the part of a pull_request payload the producing side
// needs: which pull request, which revision is its head, and what it is
// titled.
//
// The head is taken from the payload rather than from GITHUB_SHA, and the
// difference is not cosmetic. On a pull_request event the checked-out
// revision is a merge commit the platform synthesises, which exists on no
// branch and which no clearance can ever be given for. Publishing a verdict
// against it would put the status somewhere nobody looks.
//
// The title is read because a squash merge lands it as the commit message, so
// a breaking change declared there is the declaration the history keeps —
// while a declaration in a commit about to be squashed away leaves no marker
// at all.
type PullRequestEvent struct {
	Number      int `json:"number"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

// MergeGroupEvent is the part of a merge_group payload the queue-time path
// needs: the revision the queue built, the ref it built it on, and the base it
// replayed onto.
//
// A merge queue entry is the change replayed on the base branch's current tip,
// so the revision here exists on the queue's own ref and on no pull request.
// Nothing can have cleared it — a gh-readonly-queue ref carries no comment
// surface — which is why the decision the entry renders is recomputed and
// compared against the clearance the originating pull request holds. See
// docs/adr/0053-a-clearance-carries-forward-when-the-decision-it-was-given-for-is-unchanged.md.
//
// The originating pull request is named nowhere else in the payload: the
// platform encodes it in the queue ref, which QueueEntry reads.
type MergeGroupEvent struct {
	Action     string `json:"action"`
	MergeGroup struct {
		// HeadSHA is the commit the queue built, and the revision a verdict
		// about this entry is published against.
		HeadSHA string `json:"head_sha"`
		// HeadRef is the queue ref, `refs/heads/gh-readonly-queue/<base
		// branch>/pr-<number>-<sha>`.
		HeadRef string `json:"head_ref"`
		// BaseSHA is the base branch's tip the entry was replayed onto, and
		// BaseRef names that branch.
		BaseSHA string `json:"base_sha"`
		BaseRef string `json:"base_ref"`
	} `json:"merge_group"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// LoadMergeGroupEvent reads a merge_group webhook payload from disk.
func LoadMergeGroupEvent(path string) (MergeGroupEvent, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the platform's own GITHUB_EVENT_PATH or the --event flag, supplied by whoever runs lydite, not untrusted remote input
	if err != nil {
		return MergeGroupEvent{}, fmt.Errorf("reading the event payload: %w", err)
	}
	var event MergeGroupEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return MergeGroupEvent{}, fmt.Errorf("parsing the event payload: %w", err)
	}
	return event, nil
}

// QueueRefPrefix is what the platform names every merge-queue ref under, after
// the `refs/heads/` every branch ref carries.
const QueueRefPrefix = "gh-readonly-queue/"

const branchRefPrefix = "refs/heads/"

// queueEntryPattern matches the segment of a queue ref that names one entry.
// The trailing revision is the base branch's own tip when the group was
// formed, not the pull request's head — the platform builds the name out of
// what the entry was replayed onto.
var queueEntryPattern = regexp.MustCompile(`^pr-([0-9]+)-([0-9a-fA-F]{7,40})$`)

// QueueEntry is the pull request a queue ref names.
type QueueEntry struct {
	// Number is the originating pull request.
	Number int
	// BaseSHA is the revision the ref's own name carries: the base branch's
	// tip the entry was replayed onto. It is the platform's spelling of the
	// name and not a base anything is measured against — the payload's own
	// base_sha, and a locally resolved merge-base, are the revisions for that.
	BaseSHA string
}

// BaseBranch is the branch the entry is queued for, as a name rather than a
// ref.
func (e MergeGroupEvent) BaseBranch() string {
	return strings.TrimPrefix(e.MergeGroup.BaseRef, branchRefPrefix)
}

// QueueEntry reads the originating pull request out of the queue ref.
//
// The last segment is the one read. A base branch may hold slashes
// (`release/1.x`), so the segments between the prefix and the entry are the
// branch's and are not parsed; and when the queue groups several pull requests
// into one ref, the entry named is the one the platform put last.
//
// A group carrying more than one change would otherwise be only partly covered:
// the number read names the last pull request, so the clearance compared against
// is that one's, while the tree the decision is recomputed over holds every
// earlier entry's change too. referral.Fingerprint hashes the set of uncovered
// paths and the set of (Kind, Path) disqualifications, so an earlier entry whose
// referral reasons are a subset of the last's could leave the group's
// fingerprint equal to the last's own clearance — and that clearance would then
// carry forward onto a revision holding content the clearer never saw. The
// relay's own `queueBatching` (source/cloud-services/pr-relay/src/index.ts)
// closes this: it compares the queue commit's changed paths against BaseSHA
// against the pull request's own, from the same base, and answers `pending`
// rather than trusting a comparison when they differ — this type carries
// BaseSHA precisely so that check has a base to measure from.
//
// A ref that is not a queue ref, or whose last segment names no entry, is an
// error. This path exists for one event and the ref is the only place that
// event names a pull request, so a ref it cannot read leaves nothing to
// compare against rather than something to guess at.
func (e MergeGroupEvent) QueueEntry() (QueueEntry, error) {
	ref := strings.TrimPrefix(e.MergeGroup.HeadRef, branchRefPrefix)
	if !strings.HasPrefix(ref, QueueRefPrefix) {
		return QueueEntry{}, fmt.Errorf("%q is not a merge-queue ref, so it names no pull request", e.MergeGroup.HeadRef)
	}
	segments := strings.Split(strings.TrimPrefix(ref, QueueRefPrefix), "/")
	match := queueEntryPattern.FindStringSubmatch(segments[len(segments)-1])
	if match == nil {
		return QueueEntry{}, fmt.Errorf("the merge-queue ref %q does not end in a pr-<number>-<sha> segment, so it names no pull request", e.MergeGroup.HeadRef)
	}
	number, err := strconv.Atoi(match[1])
	if err != nil || number <= 0 {
		return QueueEntry{}, fmt.Errorf("the merge-queue ref %q names no pull request number", e.MergeGroup.HeadRef)
	}
	return QueueEntry{Number: number, BaseSHA: match[2]}, nil
}

// LoadPullRequestEvent reads a pull_request webhook payload from disk.
func LoadPullRequestEvent(path string) (PullRequestEvent, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the platform's own GITHUB_EVENT_PATH or the --event flag, supplied by whoever runs lydite, not untrusted remote input
	if err != nil {
		return PullRequestEvent{}, fmt.Errorf("reading the event payload: %w", err)
	}
	var event PullRequestEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return PullRequestEvent{}, fmt.Errorf("parsing the event payload: %w", err)
	}
	if event.Number == 0 {
		event.Number = event.PullRequest.Number
	}
	return event, nil
}
