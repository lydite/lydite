package forge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/clearance"
)

// Status is one commit status as a document: everything a write needs, and
// nothing about who performs it.
//
// One shape carries a verdict down either route it can take to a revision. A
// command posts it here with the job's own token, or renders it for a step
// that posts it under lydite's App identity through the relay — and the two
// say the same thing because both read these fields, rather than one transport
// re-deriving what the other decided. It is also one shape per *command*: a
// referral verdict and a clearance are the same document under a different
// context, so a single composite posts either.
//
// PullRequest travels beside SHA because the poster resolves the pull
// request's current head and refuses a revision that is not it. A document
// naming only a revision leaves the step to work out which conversation it
// belongs to, which a clearance run — whose OIDC ref is a branch, not a pull
// ref — cannot do at all.
type Status struct {
	State       clearance.State `json:"state"`
	Context     string          `json:"context"`
	Description string          `json:"description"`
	TargetURL   string          `json:"target_url,omitempty"`
	SHA         string          `json:"sha"`
	PullRequest int             `json:"pull_request"`
}

// statusDescriptionLimit is the platform's cap on a status description, in
// characters.
const statusDescriptionLimit = 140

// clipped is the document as it goes out, by either route.
//
// The description is cut here rather than at the write, so a rendered document
// carries the text a direct post would have written — a document the step that
// posts it is refused for length says nothing at all, and the refusal happens
// in a job that never decided anything.
func (s Status) clipped() Status {
	s.Description = truncate(s.Description, statusDescriptionLimit)
	return s
}

// WriteStatus renders the document at path for a separate step to post.
//
// A path that cannot be written is an error, never a warning: rendering the
// document is the whole of what the caller asked for, and a run that wrote
// nothing and carried on leaves the revision with no status — which on a pull
// request reads exactly like a gate that passed.
func WriteStatus(path string, s Status) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("writing the status document at %s: %w", path, err)
		}
	}
	raw, err := json.MarshalIndent(s.clipped(), "", "  ")
	if err != nil {
		return fmt.Errorf("writing the status document at %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing the status document at %s: %w", path, err)
	}
	return nil
}
