package releasestages

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A tag a caller passes is the most explicit statement of what is released,
// so it answers even where the ref and the checkout name another.
func TestResolveTagPrefersTheTagACallerPasses(t *testing.T) {
	dir := releaseRepo(t, releaseCommit{message: "feat: the first release", tag: "v0.2.0"})

	out, err := ResolveTag(context.Background(), ResolveTagIn{Dir: dir, Flag: "v0.3.0", RefType: "tag", RefName: "v0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tag != "v0.3.0" {
		t.Errorf("Tag = %q, want the passed v0.3.0", out.Tag)
	}
}

// The ref a run is on answers ahead of the checkout, but only when it is a
// tag: a branch build's ref name is a branch, and the checkout answers
// instead.
func TestResolveTagReadsTheRefOnlyWhenItIsATag(t *testing.T) {
	dir := releaseRepo(t, releaseCommit{message: "feat: the first release", tag: "v0.2.0"})

	for _, c := range []struct {
		refType, refName, want string
		why                    string
	}{
		{"tag", "v0.2.1", "v0.2.1", "a tag ref names the release ahead of the checkout"},
		{"branch", "main", "v0.2.0", "a branch ref is not a version, so the checked-out tag answers"},
		{"tag", "", "v0.2.0", "a tag ref with no name states nothing, so the checked-out tag answers"},
		{"", "v0.2.1", "v0.2.0", "a ref name with no type is not known to be a tag"},
	} {
		out, err := ResolveTag(context.Background(), ResolveTagIn{Dir: dir, RefType: c.refType, RefName: c.refName})
		if err != nil {
			t.Fatalf("%s: %v", c.why, err)
		}
		if out.Tag != c.want {
			t.Errorf("ResolveTag(%q, %q) = %q, want %q — %s", c.refType, c.refName, out.Tag, c.want, c.why)
		}
	}
}

// With nothing passed and no tag ref, the tag HEAD itself carries is the
// release.
func TestResolveTagFallsBackToTheCheckedOutTag(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "fix: a leader dot", tag: "v0.2.1"},
	)

	out, err := ResolveTag(context.Background(), ResolveTagIn{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tag != "v0.2.1" {
		t.Errorf("Tag = %q, want HEAD's own v0.2.1", out.Tag)
	}
}

// A commit that merely descends from a tag is not that tag, and a directory
// that is no repository names none: both leave nothing to check.
func TestResolveTagAnswersErrNoTagWhenNothingNamesOne(t *testing.T) {
	descends := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "fix: a leader dot"},
	)
	for name, in := range map[string]ResolveTagIn{
		"a commit past its tag":    {Dir: descends},
		"a branch ref":             {Dir: descends, RefType: "branch", RefName: "main"},
		"a directory with no repo": {Dir: t.TempDir()},
	} {
		out, err := ResolveTag(context.Background(), in)
		if !errors.Is(err, ErrNoTag) {
			t.Errorf("%s: err = %v, want ErrNoTag", name, err)
		}
		if out.Tag != "" {
			t.Errorf("%s: Tag = %q, want none", name, out.Tag)
		}
	}
	if got := ErrNoTag.Error(); got != "no tag to check" {
		t.Errorf("ErrNoTag = %q, want %q", got, "no tag to check")
	}
}

// The previous release is the semver-highest stable tag below this one, never
// a prerelease and never the tag itself.
func TestPreviousTagFindsTheHighestStableTagBelow(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.1.0"},
		releaseCommit{message: "feat: a second", tag: "v0.2.0"},
		releaseCommit{message: "feat: a candidate", tag: "v0.3.0-rc.1"},
		releaseCommit{message: "fix: a leader dot", tag: "v0.3.0"},
	)

	out, err := PreviousTag(context.Background(), PreviousTagIn{Dir: dir, Tag: "v0.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (PreviousTagOut{Previous: "v0.2.0"}); out != want {
		t.Errorf("PreviousTag = %+v, want %+v", out, want)
	}
}

// A full-history checkout with no tag below the target really is the first
// release, and passes as one rather than as an error.
func TestPreviousTagReportsTheFirstReleaseInAFullCheckout(t *testing.T) {
	dir := releaseRepo(t, releaseCommit{message: "feat: the first release", tag: "v0.1.0"})

	out, err := PreviousTag(context.Background(), PreviousTagIn{Dir: dir, Tag: "v0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (PreviousTagOut{First: true}); out != want {
		t.Errorf("PreviousTag = %+v, want %+v", out, want)
	}
}

// In a shallow checkout "no tag below" cannot be told from "no tag fetched",
// so it is an error naming the fetch that resolves it.
func TestPreviousTagRefusesAShallowCheckoutWithNoTagBelow(t *testing.T) {
	dir := releaseShallowClone(t, releaseRepo(t,
		releaseCommit{message: "feat: the first release"},
		releaseCommit{message: "fix: a leader dot"},
	))

	out, err := PreviousTag(context.Background(), PreviousTagIn{Dir: dir, Tag: "v0.2.0"})
	want := "v0.2.0 has no tag below it in a shallow checkout, so a real predecessor cannot be told from none at all: " +
		"check out with `fetch-depth: 0` and the repository's tags fetched"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if out != (PreviousTagOut{}) {
		t.Errorf("PreviousTag = %+v, want nothing beside the error", out)
	}
}

// A tag the range cannot be placed from is gitstate's own error, returned as
// it is rather than rephrased.
func TestPreviousTagReturnsAnInvalidTagsErrorAsItIs(t *testing.T) {
	dir := releaseRepo(t, releaseCommit{message: "feat: the first release", tag: "v0.1.0"})

	_, err := PreviousTag(context.Background(), PreviousTagIn{Dir: dir, Tag: "release-2"})
	want := `"release-2" is not a valid version — a release tag is vMAJOR.MINOR.PATCH, such as v1.4.0`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if errors.Unwrap(err) != nil {
		t.Errorf("err wraps %v, want gitstate's own error unwrapped", errors.Unwrap(err))
	}
}

// isShallow answers what git says about the checkout, and a directory git
// cannot answer for is an error rather than a "not shallow", with no answer
// beside it.
func TestIsShallowAnswersWhatGitSays(t *testing.T) {
	origin := releaseRepo(t,
		releaseCommit{message: "feat: the first release"},
		releaseCommit{message: "fix: a leader dot"},
	)
	ctx := context.Background()

	if shallow, err := isShallow(ctx, origin); err != nil || shallow {
		t.Errorf("a full clone: isShallow = %v, %v; want false, nil", shallow, err)
	}
	if shallow, err := isShallow(ctx, releaseShallowClone(t, origin)); err != nil || !shallow {
		t.Errorf("a depth-1 clone: isShallow = %v, %v; want true, nil", shallow, err)
	}
	shallow, err := isShallow(ctx, t.TempDir())
	if err == nil || !strings.HasPrefix(err.Error(), "git rev-parse --is-shallow-repository: ") {
		t.Errorf("no repository: err = %v, want the git rev-parse failure", err)
	}
	if shallow {
		t.Error("no repository: isShallow = true beside its error, want false")
	}
}
