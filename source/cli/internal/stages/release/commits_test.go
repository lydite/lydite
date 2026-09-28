package releasestages

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"lydite/lydite/internal/gitstate"
)

// The range runs from the previous release to HEAD, oldest first, and holds
// neither the previous release's own commit nor anything before it.
func TestReadCommitsReadsFromThePreviousReleaseToHead(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.1.0"},
		releaseCommit{message: "feat!: the verdict is a status"},
		releaseCommit{message: "fix: a leader dot"},
	)

	out, err := ReadCommits(context.Background(), ReadCommitsIn{Dir: dir, Previous: "v0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"feat!: the verdict is a status", "fix: a leader dot"}
	if !reflect.DeepEqual(out.Messages, want) {
		t.Errorf("Messages = %q, want %q", out.Messages, want)
	}
}

// A range whose start is not here is an error naming the fetch that resolves
// it, and it still carries git's own failure underneath.
func TestReadCommitsRefusesARangeItCannotRead(t *testing.T) {
	dir := releaseRepo(t, releaseCommit{message: "feat: the first release", tag: "v0.1.0"})
	ctx := context.Background()

	_, cause := gitstate.CommitMessages(ctx, dir, "v0.0.9", "HEAD")
	if cause == nil {
		t.Fatal("a range from a tag that does not exist must not read")
	}

	_, err := ReadCommits(ctx, ReadCommitsIn{Dir: dir, Previous: "v0.0.9"})
	want := "the commits in v0.0.9..HEAD cannot be read: " + cause.Error() +
		"\n       v0.0.9 must exist here as a tag and its commit must be present and reachable from HEAD:" +
		"\n       check out with `fetch-depth: 0` and the repository's tags fetched"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Errorf("err does not wrap git's own exit: %v", err)
	}
}
