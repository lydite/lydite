package releasestages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/gitstate"
)

// ReadCommitsIn names the release the range starts from, and where.
type ReadCommitsIn struct {
	Dir      string
	Previous string
}

// ReadCommitsOut is the message of every commit in the range, oldest first.
type ReadCommitsOut struct {
	Messages []string
}

// ReadCommits reads the messages of the commits a release carries.
//
// The range ends at HEAD, never at the tag string. A tag-triggered run is
// checked out detached at the pushed tag, so HEAD is that tag's commit — and
// reading to HEAD is also what lets a release be checked from the commit it is
// about to be cut at, before the tag object exists.
func ReadCommits(ctx context.Context, in ReadCommitsIn) (ReadCommitsOut, error) {
	messages, err := gitstate.CommitMessages(ctx, in.Dir, in.Previous, "HEAD")
	if err != nil {
		return ReadCommitsOut{}, fmt.Errorf("the commits in %s..HEAD cannot be read: %w"+
			"\n       %s must exist here as a tag and its commit must be present and reachable from HEAD:"+
			"\n       check out with `fetch-depth: 0` and the repository's tags fetched", in.Previous, err, in.Previous)
	}
	return ReadCommitsOut{Messages: messages}, nil
}
