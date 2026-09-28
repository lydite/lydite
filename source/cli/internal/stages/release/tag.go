// Package releasestages holds the stages that check the tag a release is about
// to publish: resolving the tag, finding the release before it, reading the
// commits between the two, and judging whether a break any of them declares
// lands on a bump that admits one. See
// docs/adr/0045-a-tag-that-is-not-the-breaking-bump-cannot-carry-a-declared-break.md.
//
// Every stage is a plain function of its own In. Nothing here reads the
// environment: the ref a run was triggered by arrives as fields, and nothing
// here prints or decides a row.
package releasestages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
)

// ErrNoTag is ResolveTag's answer when neither the caller, the ref the run is
// on, nor the checkout names a tag. The caller decides how to tell a person
// where a tag could have come from.
var ErrNoTag = errors.New("no tag to check")

// ResolveTagIn is every place the tag being released may be stated.
type ResolveTagIn struct {
	// Dir is the repository whose checked-out commit is asked last.
	Dir string
	// Flag is the tag a caller passed explicitly.
	Flag string
	// RefType and RefName are the type and short name of the ref the run was
	// triggered by.
	RefType string
	RefName string
}

// ResolveTagOut is the tag being released.
type ResolveTagOut struct {
	Tag string
}

// ResolveTag is the tag being released, in descending order of how explicitly
// it was stated: what a caller passed, then the tag the workflow run is on,
// then the tag checked out here.
//
// The ref name the caller passes is the short name of whatever ref triggered a
// run, so it is read only when the ref type the caller passes says that ref is
// a tag — on a branch build it holds a branch name, and checking a branch name
// as a version would report a misconfiguration as a malformed tag.
func ResolveTag(ctx context.Context, in ResolveTagIn) (ResolveTagOut, error) {
	if in.Flag != "" {
		return ResolveTagOut{Tag: in.Flag}, nil
	}
	if in.RefType == "tag" && in.RefName != "" {
		return ResolveTagOut{Tag: in.RefName}, nil
	}
	// --exact-match, so a commit that merely descends from a tag resolves to
	// nothing: the check is about the tag this commit is, not the one before
	// it.
	if r := executil.RunQuiet(ctx, in.Dir, "git", "describe", "--tags", "--exact-match", "HEAD"); r.Ok() {
		return ResolveTagOut{Tag: strings.TrimSpace(r.Output)}, nil
	}
	return ResolveTagOut{}, ErrNoTag
}

// PreviousTagIn names the tag whose predecessor is looked for, and where.
type PreviousTagIn struct {
	Dir string
	Tag string
}

// PreviousTagOut is the release before Tag, or First when there is none.
type PreviousTagOut struct {
	// Previous is the semver-highest stable tag below Tag; empty when First.
	Previous string
	// First reports that Tag is the repository's first release, so its range
	// is empty.
	First bool
}

// PreviousTag finds the release Tag's range starts from.
//
// An invalid tag arrives back as an error rather than a verdict: a range this
// cannot place is a check that did not run, and the alternative is publishing
// a tag nobody looked at.
func PreviousTag(ctx context.Context, in PreviousTagIn) (PreviousTagOut, error) {
	previous, ok, err := gitstate.PreviousTag(ctx, in.Dir, in.Tag)
	if err != nil {
		return PreviousTagOut{}, err
	}
	if ok {
		return PreviousTagOut{Previous: previous}, nil
	}
	// A shallow checkout with no tags below the target reads exactly like a
	// genuine first release — both have no candidate to find. Only a shallow
	// repository is asked here, because a full-history checkout with no lower
	// tag really is the first release, and reporting that as an error would
	// refuse a case ADR 0045 states must pass.
	shallow, err := isShallow(ctx, in.Dir)
	if err != nil {
		return PreviousTagOut{}, err
	}
	if shallow {
		return PreviousTagOut{}, fmt.Errorf("%s has no tag below it in a shallow checkout, so a real predecessor cannot be told from none at all: "+
			"check out with `fetch-depth: 0` and the repository's tags fetched", in.Tag)
	}
	return PreviousTagOut{First: true}, nil
}

// isShallow reports whether dir is a shallow checkout — one that may hold
// commits without holding the tags that decorate its own history, which is
// exactly the shape that makes "no previous tag" ambiguous between a genuine
// first release and one the checkout never fetched.
func isShallow(ctx context.Context, dir string) (bool, error) {
	r := executil.RunQuiet(ctx, dir, "git", "rev-parse", "--is-shallow-repository")
	if !r.Ok() {
		return false, fmt.Errorf("git rev-parse --is-shallow-repository: %w", r.Err)
	}
	return strings.TrimSpace(r.Output) == "true", nil
}
