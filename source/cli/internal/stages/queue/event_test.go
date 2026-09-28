package queuestages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The entry, the revision the verdict is published against, the ref the relay
// checks its claim against and the branch the base is resolved from all come
// out of the one payload.
func TestLoadEventReadsTheEntryThePayloadNames(t *testing.T) {
	path := queueEvent(t, 69, "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567", "release/1.x")

	out, err := LoadEvent(context.Background(), LoadEventIn{EventPath: path})
	if err != nil {
		t.Fatal(err)
	}
	want := LoadEventOut{
		PullRequest: 69,
		HeadSHA:     "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567",
		HeadRef:     "refs/heads/gh-readonly-queue/release/1.x/pr-69-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
		BaseBranch:  "release/1.x",
	}
	if out != want {
		t.Errorf("LoadEvent = %+v, want %+v", out, want)
	}
}

// A payload that is not a merge_group's names no queue revision, and there is
// nothing to publish a verdict against.
func TestLoadEventRefusesAPayloadNamingNoQueueRevision(t *testing.T) {
	path := queueEventFile(t, `{"pull_request":{"number":9,"head":{"sha":"abc"}}}`)

	_, err := LoadEvent(context.Background(), LoadEventIn{EventPath: path})
	want := path + " names no merge group: this command answers a merge_group event"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A merge group naming a revision but no entry has no pull request to read a
// clearance off.
func TestLoadEventRefusesARefThatNamesNoEntry(t *testing.T) {
	path := queueEventFile(t, `{
	  "merge_group": {
	    "head_sha": "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef",
	    "head_ref": "refs/heads/gh-readonly-queue/main",
	    "base_ref": "refs/heads/main"
	  }
	}`)

	_, err := LoadEvent(context.Background(), LoadEventIn{EventPath: path})
	want := `the merge-queue ref "refs/heads/gh-readonly-queue/main" does not end in a pr-<number>-<sha> segment, so it names no pull request`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A payload that is not there is refused naming the read, so a workflow that
// wrote it elsewhere is told where it was looked for.
func TestLoadEventRefusesAPayloadItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")

	_, err := LoadEvent(context.Background(), LoadEventIn{EventPath: path})
	want := "reading the event payload: open " + path + ": no such file or directory"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap os.ErrNotExist", err)
	}
}

// A payload that is not JSON is refused naming the parse.
func TestLoadEventRefusesAPayloadThatDoesNotParse(t *testing.T) {
	path := queueEventFile(t, `{"merge_group":`)

	_, err := LoadEvent(context.Background(), LoadEventIn{EventPath: path})
	want := "parsing the event payload: unexpected end of JSON input"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
