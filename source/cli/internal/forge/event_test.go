package forge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A payload only points: the comment's id and the repository it claims are
// read, and the body, author, time and thread it also carries are not, so
// nothing downstream can decide from the payload's copy of the comment.
func TestReadCommentRefReadsOnlyTheIDAndTheClaimedRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	payload := `{
	  "action": "created",
	  "issue": {"number": 40, "pull_request": {"url": "https://api.github.com/repos/lydite/lydite/pulls/40"}},
	  "comment": {"id": 9001, "body": "/lydite clear", "created_at": "2026-08-31T12:00:00Z", "user": {"login": "pedromvgomes"}},
	  "repository": {"full_name": "lydite/lydite"}
	}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCommentRef(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CommentRef{ID: 9001, Repository: "lydite/lydite"}); got != want {
		t.Errorf("ReadCommentRef = %+v, want %+v", got, want)
	}
}

// The claimed repository is read as the payload spells it, whatever it names:
// comparing it against the trusted one is the caller's to do, and a reader
// that normalised or dropped it would hide exactly the mismatch to refuse.
func TestReadCommentRefKeepsAClaimNamingAnotherRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"comment":{"id":1},"repository":{"full_name":"someone/else"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCommentRef(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repository != "someone/else" {
		t.Errorf("Repository = %q, want the payload's own claim", got.Repository)
	}
}

// A payload that names no comment has nothing to resolve, and one that names
// no repository has no claim to check; each is refused, and a payload that
// cannot be read or parsed says which of the two it was.
func TestReadCommentRefRefusesAPayloadItCannotUse(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "no comment", payload: `{"repository":{"full_name":"lydite/lydite"}}`, want: "names no comment"},
		{name: "no comment id", payload: `{"comment":{"body":"/lydite clear"},"repository":{"full_name":"lydite/lydite"}}`, want: "names no comment"},
		{name: "no repository", payload: `{"comment":{"id":1}}`, want: "names no repository"},
		{name: "malformed", payload: `{"comment":`, want: "parsing the event payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "event.json")
			if err := os.WriteFile(path, []byte(tc.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadCommentRef(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want a refusal carrying %q", err, tc.want)
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "absent.json")
	if _, err := ReadCommentRef(missing); err == nil || !strings.Contains(err.Error(), "reading the event payload") {
		t.Errorf("err = %v, want a refusal naming the read", err)
	}
}

// The title is what a squash merge lands, so a breaking change declared there
// is the one the history keeps — a payload read without it would report the
// declaration missing.
func TestLoadPullRequestEventReadsTheTitleAndTheHead(t *testing.T) {
	payload := `{
	  "number": 7,
	  "pull_request": {
	    "number": 7,
	    "title": "feat!: drop the v1 client",
	    "head": {"sha": "c0ffee"}
	  }
	}`
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	event, err := LoadPullRequestEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	if event.PullRequest.Title != "feat!: drop the v1 client" {
		t.Errorf("Title = %q, want the title the payload carries", event.PullRequest.Title)
	}
	if event.Number != 7 || event.PullRequest.Head.SHA != "c0ffee" {
		t.Errorf("event = %+v, want pull request 7 at c0ffee", event)
	}
}

// A payload carrying no title is a pull request with an empty one, not a load
// error: every other field a caller needs is still there, and a title nobody
// wrote declares nothing.
func TestLoadPullRequestEventWithoutATitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"pull_request":{"number":9,"head":{"sha":"abc"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	event, err := LoadPullRequestEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	if event.PullRequest.Title != "" || event.Number != 9 {
		t.Errorf("event = %+v, want number 9 taken from the pull request and no title", event)
	}
}

// The realistic payload, abbreviated to the fields read: the platform names the
// originating pull request nowhere but the queue ref, so a payload parsed
// without reading that ref names nothing to compare a clearance against.
const mergeGroupPayload = `{
  "action": "checks_requested",
  "merge_group": {
    "head_sha": "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567",
    "head_ref": "refs/heads/gh-readonly-queue/main/pr-69-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
    "head_commit": {"id": "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567", "message": "feat: a change"},
    "base_sha": "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
    "base_ref": "refs/heads/main"
  },
  "repository": {"full_name": "vipengele/typescript"},
  "sender": {"login": "octocat"}
}`

func TestLoadMergeGroupEventReadsTheQueueRevisionAndItsEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(mergeGroupPayload), 0o600); err != nil {
		t.Fatal(err)
	}

	event, err := LoadMergeGroupEvent(path)
	if err != nil {
		t.Fatal(err)
	}
	if event.MergeGroup.HeadSHA != "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567" {
		t.Errorf("HeadSHA = %q, want the revision the queue built", event.MergeGroup.HeadSHA)
	}
	if event.MergeGroup.BaseSHA != "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d" {
		t.Errorf("BaseSHA = %q, want the tip the entry was replayed onto", event.MergeGroup.BaseSHA)
	}
	if event.Repository.FullName != "vipengele/typescript" {
		t.Errorf("FullName = %q, want the repository the payload names", event.Repository.FullName)
	}
	if got := event.BaseBranch(); got != "main" {
		t.Errorf("BaseBranch() = %q, want the branch without its ref prefix", got)
	}
	entry, err := event.QueueEntry()
	if err != nil {
		t.Fatal(err)
	}
	if entry.Number != 69 {
		t.Errorf("Number = %d, want the pull request the queue ref names", entry.Number)
	}
	// The revision the name carries is the base the entry was replayed onto,
	// not the pull request's head: a caller reading it as a head would compare
	// a clearance against a revision no clearance was given for.
	if entry.BaseSHA != "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d" {
		t.Errorf("BaseSHA = %q, want the revision the ref's own name carries", entry.BaseSHA)
	}
}

// A base branch may hold slashes, and the segments between the prefix and the
// entry are the branch's rather than something to parse.
func TestQueueEntryReadsTheLastSegmentOfANestedBaseBranch(t *testing.T) {
	var event MergeGroupEvent
	event.MergeGroup.HeadRef = "refs/heads/gh-readonly-queue/release/1.x/pr-412-deadbeefdeadbeef"

	entry, err := event.QueueEntry()
	if err != nil {
		t.Fatal(err)
	}
	if entry.Number != 412 {
		t.Errorf("Number = %d, want 412", entry.Number)
	}
}

// A ref this command cannot read names no pull request, and nothing about a
// clearance can be decided from a guess at which one it was.
// A payload that could not be read at all and one that is not JSON are both
// "there is no merge group here", and each names which of the two it was: a
// workflow that wrote the event to another path and one that wrote something
// other than the platform's own document are different things to go and fix.
func TestLoadMergeGroupEventRefusesAPayloadItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	if _, err := LoadMergeGroupEvent(missing); err == nil ||
		!strings.Contains(err.Error(), "reading the event payload") {
		t.Errorf("err = %v, want a refusal naming the read", err)
	}

	malformed := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(malformed, []byte(`{"merge_group":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMergeGroupEvent(malformed); err == nil ||
		!strings.Contains(err.Error(), "parsing the event payload") {
		t.Errorf("err = %v, want a refusal naming the parse", err)
	}
}

func TestQueueEntryRefusesARefThatNamesNoEntry(t *testing.T) {
	for _, ref := range []string{
		"refs/heads/main",
		"refs/heads/gh-readonly-queue/main",
		"refs/heads/gh-readonly-queue/main/pr-69",
		"refs/heads/gh-readonly-queue/main/pr-0-deadbeefdead",
		"refs/heads/gh-readonly-queue/main/pr-69-zz",
		"",
	} {
		var event MergeGroupEvent
		event.MergeGroup.HeadRef = ref
		if entry, err := event.QueueEntry(); err == nil {
			t.Errorf("QueueEntry() on %q = %+v, want a refusal", ref, entry)
		}
	}
}
