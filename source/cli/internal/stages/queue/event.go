// Package queuestages holds the stages that carry a clearance forward onto a
// merge-queue entry: reading the entry out of the merge_group payload,
// resolving the base the decision is recomputed against, recomputing and
// fingerprinting that decision, minting the OIDC token the relay accepts, and
// submitting the comparison. They are the queue flow's stages over
// internal/relay, which is the transport; there is no queue domain package of
// its own.
//
// Every stage is a plain function of its own In. Nothing here reads the
// environment: the payload's path, the mint endpoint and the token that
// authorises the mint all arrive as fields, and nothing here prints.
package queuestages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/forge"
)

// LoadEventIn names the merge_group payload LoadEvent reads.
type LoadEventIn struct {
	EventPath string
}

// LoadEventOut is the entry the payload names, and the facts about it every
// later stage reads one at a time.
type LoadEventOut struct {
	// PullRequest is the originating pull request the queue ref names.
	PullRequest int
	// HeadSHA is the revision the queue built, and the one a verdict is
	// published against.
	HeadSHA string
	// HeadRef is the queue ref exactly as the payload carries it, which the
	// relay checks against the ref its verified claim carries.
	HeadRef string
	// BaseBranch is the branch the entry is queued for, as a name rather
	// than a ref.
	BaseBranch string
}

// LoadEvent reads the merge_group payload and the entry its queue ref names.
//
// A payload naming no queue revision is refused, because there is nothing to
// publish a verdict against; so is a ref naming no entry, because the ref is
// the only place the event names a pull request, and without one there is no
// clearance to compare against.
func LoadEvent(_ context.Context, in LoadEventIn) (LoadEventOut, error) {
	event, err := forge.LoadMergeGroupEvent(in.EventPath)
	if err != nil {
		return LoadEventOut{}, err
	}
	if event.MergeGroup.HeadSHA == "" {
		return LoadEventOut{}, fmt.Errorf("%s names no merge group: this command answers a merge_group event", in.EventPath)
	}
	entry, err := event.QueueEntry()
	if err != nil {
		return LoadEventOut{}, err
	}
	return LoadEventOut{
		PullRequest: entry.Number,
		HeadSHA:     event.MergeGroup.HeadSHA,
		HeadRef:     event.MergeGroup.HeadRef,
		BaseBranch:  event.BaseBranch(),
	}, nil
}
