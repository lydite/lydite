package main

import (
	"context"
	"fmt"
	"strings"

	"lydite/lydite/internal/depdelta"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// gateDependencies labels each manifest's verdict in the report.
const gateDependencies = "dependencies"

// addDependencyRows compares the packages every dependency manifest the change
// touches pins at the merge-base against the ones it pins at HEAD, and folds
// the answer into the report and into the decision.
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
//
// Which paths are manifests is sniffed from the diff, never from a component
// declaration: the question is "does this change touch a manifest, and of what
// kind", which the changed paths answer completely, and reading a component's
// declared lockfile instead would miss a manifest no component declared.
func addDependencyRows(ctx context.Context, report *ui.Report, d *referral.Decision, dir, base string, paths []string) {
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
			referUnmeasured(d, p, fmt.Sprintf("no dependency reader for %s", m.Ecosystem()))
			continue
		}
		baseSet, err := dependenciesAt(ctx, dir, base, p, m, shortSHA(base))
		if err != nil {
			referUnmeasured(d, p, err.Error())
			continue
		}
		headSet, err := dependenciesAt(ctx, dir, "HEAD", p, m, "HEAD")
		if err != nil {
			referUnmeasured(d, p, err.Error())
			continue
		}

		delta := depdelta.Compare(baseSet, headSet)
		if len(delta.Added) > 0 {
			refer(d, referral.Disqualification{
				Kind:     referral.DisqualificationDependencyAdded,
				Path:     p,
				Evidence: fmt.Sprintf("%s pins %s", p, strings.Join(capped(delta.Added), ", ")),
			})
			continue
		}
		// A manifest that was compared and added nothing says so under its own
		// name: a manifest missing from the report is indistinguishable from
		// one nothing was measured over.
		report.Add(ui.Row{
			Status: ui.StatusPass,
			Label:  gateDependencies + "(" + p + ")",
			Value:  "no package added against " + shortSHA(base),
		})
	}
}

// referUnmeasured refers a manifest whose dependency set could not be built,
// naming what stopped it.
func referUnmeasured(d *referral.Decision, p, reason string) {
	refer(d, referral.Disqualification{
		Kind:     referral.DisqualificationDependencyDeltaUnmeasured,
		Path:     p,
		Evidence: p + ": " + reason,
	})
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
