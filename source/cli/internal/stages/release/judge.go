package releasestages

import (
	"context"
	"strings"

	"golang.org/x/mod/semver"

	"lydite/lydite/internal/declaration"
)

// JudgeIn is the release range and the messages of the commits it carries.
type JudgeIn struct {
	Previous string
	Tag      string
	Messages []string
}

// JudgeOut is what the range declares and whether the bump admits it. It
// decides no verdict of its own: which row that makes is the caller's.
type JudgeOut struct {
	// Declaring is the subject of every commit whose message declares a
	// break, in the order the commits were read.
	Declaring []string
	// Count is how many commits the range holds.
	Count int
	// Range is the version range the verdict is about, Previous..Tag.
	Range string
	// Admits reports whether moving from Previous to Tag is a bump a declared
	// break may land on.
	Admits bool
}

// Judge collects the commits in a range that declare a break, and whether the
// bump the range spans admits one.
func Judge(_ context.Context, in JudgeIn) (JudgeOut, error) {
	var declaring []string
	for _, message := range in.Messages {
		if declaration.Declared(message) {
			declaring = append(declaring, subject(message))
		}
	}
	// The range named is the version range the verdict is about, which is what
	// a consumer upgrades along. HEAD is the revision the commits were read to
	// and is a ref that moves, so naming it would describe the release by
	// something that no longer points there when anybody looks.
	return JudgeOut{
		Declaring: declaring,
		Count:     len(in.Messages),
		Range:     in.Previous + ".." + in.Tag,
		Admits:    bumpAdmitsBreak(in.Previous, in.Tag),
	}, nil
}

// subject is a commit's first line, which is what identifies it to a person
// reading the failure. The whole message is what declaration reads, because a
// break may be declared in a footer.
func subject(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(line)
}

// bumpAdmitsBreak reports whether moving from previous to tag increases the
// leftmost non-zero component of previous, which is the bump ADR 0010 requires
// a declared break to land on.
//
// The comparison is over the prefix through that component rather than over
// the component alone, so a release crossing eras answers correctly:
// v0.2.0 → v1.0.0 admits a break even though the minor it is measured on went
// from 2 to 0.
func bumpAdmitsBreak(previous, tag string) bool {
	p, t := releaseVersion(previous), releaseVersion(tag)
	switch {
	case semver.Major(p) != "v0":
		return semver.Compare(semver.Major(t), semver.Major(p)) > 0
	case semver.MajorMinor(p) != "v0.0":
		return semver.Compare(semver.MajorMinor(t), semver.MajorMinor(p)) > 0
	default:
		return semver.Compare(t, p) > 0
	}
}

// releaseVersion is the version a tag releases, with any prerelease and build
// metadata dropped: v0.3.0-rc.1 is the 0.3.0 line reaching a consumer early,
// and the bump it carries is 0.3.0's.
func releaseVersion(tag string) string {
	canonical := semver.Canonical(tag)
	return strings.TrimSuffix(canonical, semver.Prerelease(canonical))
}
