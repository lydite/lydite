package reviewdecision

import (
	"context"
	"fmt"
	"strings"

	"lydite/lydite/internal/depdelta"
	"lydite/lydite/internal/referral"
)

// ManifestDelta is what the change did to one dependency manifest, or why that
// could not be said.
//
// The two are one type because they are one answer to one question, read by
// both the disqualifier and the version condition. A delta that was never
// built and a delta that moved nothing must never be the same value: the first
// is a manifest lydite could not read, and the second is a manifest it read.
type ManifestDelta struct {
	// Path is the manifest, from the repository root.
	Path string
	// Delta is the comparison, valid only when Unmeasured is empty.
	Delta depdelta.Delta
	// Unmeasured is what stopped the comparison, empty when it stands.
	Unmeasured string
}

// measureDependencies compares the packages every dependency manifest the
// change touches pins at the merge-base against the ones it pins at HEAD.
//
// Which paths are manifests is sniffed from the diff, never from a component
// declaration: the question is "does this change touch a manifest, and of what
// kind", which the changed paths answer completely, and reading a component's
// declared lockfile instead would miss a manifest no component declared.
//
// Measuring is separate from referring because the answer decides two things
// in two places — the disqualifier, and whether a conditional exemption's
// versions all moved a little — and one manifest read twice is two answers to
// one question.
func measureDependencies(ctx context.Context, dir, base string, paths []string) []ManifestDelta {
	var out []ManifestDelta
	seen := map[string]bool{}
	for _, p := range paths {
		m := depdelta.Detect(p)
		// Both sides of a rename are changed paths, and a manifest renamed
		// onto itself would otherwise be compared and reported twice.
		if m == depdelta.ManifestNone || seen[p] {
			continue
		}
		seen[p] = true

		if !m.Readable() {
			out = append(out, ManifestDelta{Path: p, Unmeasured: fmt.Sprintf("no dependency reader for %s", m.Ecosystem())})
			continue
		}
		baseSet, err := dependenciesAt(ctx, dir, base, p, m, short(base))
		if err != nil {
			out = append(out, ManifestDelta{Path: p, Unmeasured: err.Error()})
			continue
		}
		headSet, err := dependenciesAt(ctx, dir, "HEAD", p, m, "HEAD")
		if err != nil {
			out = append(out, ManifestDelta{Path: p, Unmeasured: err.Error()})
			continue
		}
		out = append(out, ManifestDelta{Path: p, Delta: depdelta.Compare(baseSet, headSet)})
	}
	return out
}

// referDependencies folds each manifest's comparison into the decision.
//
// A package at HEAD that the merge-base did not pin refers, direct or
// transitive alike, and no version move on a name already there does. A
// removal never refers on its own: it is the opposite of new untrusted code
// arriving, and referring it would send every dependency cleanup to a person
// in exchange for nothing. See docs/adr/0047.
//
// A manifest whose set could not be built at either side refers too, under its
// own kind. An ecosystem with no reader and a lockfile that did not parse are
// both states in which lydite cannot say whether a dependency was added, and
// reporting that as "it added none" is the one direction the comparison exists
// to avoid.
func referDependencies(d *referral.Decision, deltas []ManifestDelta) {
	for _, m := range deltas {
		if m.Unmeasured != "" {
			refer(d, referral.Disqualification{
				Kind:     referral.DisqualificationDependencyDeltaUnmeasured,
				Path:     m.Path,
				Evidence: m.Path + ": " + m.Unmeasured,
			})
			continue
		}
		if len(m.Delta.Added) > 0 {
			refer(d, referral.Disqualification{
				Kind:     referral.DisqualificationDependencyAdded,
				Path:     m.Path,
				Evidence: fmt.Sprintf("%s pins %s", m.Path, strings.Join(Capped(m.Delta.Added), ", ")),
			})
		}
	}
}

// versionsPatchAndMinor reports whether every version pair in every manifest
// the change touches moved by a patch or a minor.
//
// A manifest that could not be measured fails it, for the reason it also
// disqualifies: lydite cannot say how far a version moved in a file it could
// not read, so it does not say it moved a little. A change touching no
// manifest at all has no pair to disqualify it and passes this half, leaving
// the licence and SCA evidence to decide.
func versionsPatchAndMinor(deltas []ManifestDelta) bool {
	for _, m := range deltas {
		if m.Unmeasured != "" || !m.Delta.PatchOrMinorEligible() {
			return false
		}
	}
	return true
}

// dependenciesAt is the set of packages a manifest pins at one revision.
//
// A revision that does not have the path pins nothing, which is what a
// manifest this change introduces means: every package in it is at HEAD and at
// no base, so every one of them is added. A revision that has it and cannot
// read it is the error, and refers.
//
// Both sides are read out of a commit rather than off disk, because the
// verdict has to be the one CI reaches: referral scores committed state, and a
// working tree with an uncommitted lockfile edit must not change the answer.
func dependenciesAt(ctx context.Context, dir, rev, repoPath string, m depdelta.Manifest, where string) (depdelta.Set, error) {
	content, present, err := showAtRevision(ctx, dir, rev, repoPath)
	if err != nil {
		return depdelta.Set{}, err
	}
	if !present {
		return depdelta.Set{}, nil
	}
	return depdelta.Extract(m, content, repoPath+" at "+where)
}
