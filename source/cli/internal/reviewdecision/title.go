package reviewdecision

import (
	"fmt"

	"lydite/lydite/internal/forge"
)

// PullRequestTitle reads the title out of the webhook payload at eventPath,
// and is empty wherever there is no payload to read. It returns the warnings
// the read raised, each a whole line without its newline.
//
// An empty path is no pull request, not an error: a local review has none,
// and the commits carry the declaration there. Which path to read is the
// caller's to resolve; nothing here consults the environment. A payload that
// exists and cannot be read is warned about rather than fatal, for the same
// reason — the title can only add a referral, so failing the run over an
// unreadable one would turn an additive source into a blocker.
func PullRequestTitle(eventPath string) (string, []string) {
	if eventPath == "" {
		return "", nil
	}
	event, err := forge.LoadPullRequestEvent(eventPath)
	if err != nil {
		return "", []string{fmt.Sprintf("warning: could not read the event at %s (%v) — a break declared only in the pull request title is not seen", eventPath, err)}
	}
	return event.PullRequest.Title, nil
}
