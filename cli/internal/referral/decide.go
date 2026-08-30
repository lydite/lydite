package referral

import "path"

// Decision is the outcome of evaluating one change against the exemption
// set.
type Decision struct {
	// Referred is the verdict. False means the change may merge unattended.
	Referred bool
	// Exemption names the entry that covered the change, empty when none
	// did. It is reported even on a referral, because "matched, then vetoed"
	// and "matched nothing" are different things to fix.
	Exemption string
	// Uncovered lists the changed paths no single exemption covered. Empty
	// when an exemption matched, or when the change touched nothing.
	Uncovered []string
	// Disqualifications are the vetoes the change tripped.
	Disqualifications []Disqualification
	// Empty is true when the change touches no paths at all.
	Empty bool
	// Bundled is the paths a change touches alongside the exemptions file,
	// empty when it does not touch it or touches nothing else. It is the
	// only thing this package reports that is the author's to clear.
	Bundled []string
}

// Decide evaluates a change against an exemptions file.
//
// It is fail-closed: no exemption matching means refer, so an empty file —
// the day-one state — refers everything. That is the correct starting point
// rather than a transitional one. Each exemption added afterwards is an
// explicit, versioned statement of what may merge unread, which is the
// artefact worth having.
//
// Matching is against a single exemption. A change every one of whose paths
// is covered, but only by two different exemptions between them, is referred:
// the union of two declared shapes is a third shape nobody declared. See the
// Exemption doc comment.
func Decide(ch Change, file File) Decision {
	d := Decision{
		Disqualifications: Disqualifications(ch, file.Disqualifiers),
		Bundled:           bundledWithExemptions(ch.Paths),
	}

	// A change that touches nothing cannot be dangerous, and there is
	// nothing for a human to read. Every exemption would cover it
	// vacuously, so leaving it to the loop below would make the verdict
	// depend on whether the file happens to be empty — a pass on a repo with
	// one exemption, a referral on a repo with none, for the same absence of
	// a change.
	if len(ch.Paths) == 0 {
		d.Empty = true
		return d
	}

	for _, e := range file.Exemptions {
		if e.Covers(ch.Paths) {
			d.Exemption = e.Name
			break
		}
	}
	if d.Exemption == "" {
		d.Uncovered = uncovered(ch.Paths, file.Exemptions)
		d.Referred = true
		return d
	}
	d.Referred = len(d.Disqualifications) > 0
	return d
}

// bundledWithExemptions returns the paths a change touches besides the
// exemption set itself.
//
// A pull request that edits the exemptions file must edit nothing else, and
// unlike everything else here that is a gate rather than a referral: the
// author clears it by splitting the change in two, which is work they can
// do and work worth doing.
//
// Two properties already protect this file — the gate reads the merge-base's
// copy, so a change gets no benefit from its own widening, and touching it
// is a disqualifier, so such a change is always referred. Neither closes the
// realistic attack, which is not a forged exemption but an unremarkable one
// riding along: a small widening bundled into a large change that a reviewer
// approves for its other contents. The widening is permanent and every later
// change of that shape is exempt, and nobody read it. Prominence was the
// alternative and it relies on careful reading, which is the thing this
// whole design assumes will not happen.
//
// What isolation buys is that `git log` on the exemptions file becomes the
// complete, reviewable record of every widening — the audit artefact the
// allowlist model exists to produce.
//
// .lydite.yml deliberately carries no such requirement: report paths change
// alongside code for honest reasons, and a rule that fires on ordinary work
// is one that gets relaxed later.
func bundledWithExemptions(paths []string) []string {
	touchesExemptions := false
	var others []string
	for _, p := range paths {
		if path.Base(p) == FileName {
			touchesExemptions = true
			continue
		}
		others = append(others, p)
	}
	if !touchesExemptions {
		return nil
	}
	return others
}

// uncovered reports the paths that no exemption covered, so a referral can
// name what stood in the way rather than only that something did.
//
// A path is listed when *no* exemption matches it. That is a weaker test than
// the all-or-nothing rule Decide applies, and deliberately so: it answers
// "which paths would I have to declare?" rather than restating the verdict.
// A change whose paths are each covered by some exemption, but by no single
// one, therefore lists nothing here — and the report says so in those words
// rather than showing an empty list.
func uncovered(paths []string, exemptions []Exemption) []string {
	var out []string
	for _, p := range paths {
		matched := false
		for _, e := range exemptions {
			if e.Covers([]string{p}) {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, p)
		}
	}
	return out
}
