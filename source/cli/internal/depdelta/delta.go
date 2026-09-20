package depdelta

import "slices"

// Change is one package present at both sides of a comparison, pinned at a
// different version, a different origin, or both.
type Change struct {
	// Name is the package, as its own ecosystem spells it.
	Name string
	// Base is every version the merge-base pinned it at, sorted.
	Base []string
	// Head is every version HEAD pins it at, sorted.
	Head []string
	// BaseOrigin and HeadOrigin are the origin recorded for Base[0] and
	// Head[0] — Cargo's `source`, npm's `resolved`, a go.sum content hash —
	// populated only when there is exactly one pin on each side, since an
	// origin belongs to one (name, version) pair and not to a package pinned
	// at several. Empty when the ecosystem names no origin at all.
	BaseOrigin string
	HeadOrigin string
}

// Delta is what a change did to one manifest's dependencies.
type Delta struct {
	// Added is every package at HEAD that the merge-base did not pin,
	// sorted. No distinction is drawn between a direct and a transitive
	// dependency: new code entering the tree arrives mostly transitively, and
	// a count of direct additions only misses it.
	Added []string
	// Removed is every package the merge-base pinned that HEAD does not,
	// sorted. It is reported and never gated on: a removal is the opposite of
	// new untrusted code arriving.
	Removed []string
	// Changed is every package both sides pin, at versions that differ.
	Changed []Change
}

// Compare is what moved between two sides of one manifest.
//
// A package is added when its *name* is absent at the base, so a version move
// on a name already there is never an addition — that is the version
// classification's business and not this distinction's.
func Compare(base, head Set) Delta {
	d := Delta{}
	for _, name := range head.Names() {
		if !base.Has(name) {
			d.Added = append(d.Added, name)
			continue
		}
		b, h := base.Versions(name), head.Versions(name)
		var bo, ho string
		if len(b) == 1 {
			bo = base.Origin(name, b[0])
		}
		if len(h) == 1 {
			ho = head.Origin(name, h[0])
		}
		// A pin keeping its name AND its version while its origin moves is a
		// change this comparison has to see, even though the version lists
		// alone read as identical: a package wearing the same label can still
		// be a different install of it. A version that does move is not held
		// to this — npm's `resolved` names the tarball for that version, so
		// an ordinary bump changes it as a matter of course, and comparing it
		// against a different version's origin answers a question nobody
		// asked.
		sameVersionDifferentOrigin := len(b) == 1 && len(h) == 1 && b[0] == h[0] && bo != ho
		if !slices.Equal(b, h) || sameVersionDifferentOrigin {
			d.Changed = append(d.Changed, Change{Name: name, Base: b, Head: h, BaseOrigin: bo, HeadOrigin: ho})
		}
	}
	for _, name := range base.Names() {
		if !head.Has(name) {
			d.Removed = append(d.Removed, name)
		}
	}
	return d
}

// PatchOrMinorEligible reports whether this package's move is the routine
// maintenance a `versions: patch-and-minor` exemption may cover.
//
// A package pinned at more than one version on either side is not eligible.
// Which of two pins moved to which is not something the manifest's text says,
// so there is no pair to classify, and picking one would be measuring a
// distance nobody stated.
//
// A pin keeping its version while its origin changed is not eligible. A
// package whose name and version are identical at both sides but whose
// `source` or `resolved` moves to a different registry, git remote or
// tarball is not the routine maintenance the condition exists to wave
// through — it is exactly the new untrusted code entering the tree that #23
// is about, wearing a label that makes the diff read as boring. A version
// that did move is judged on the version alone: an origin naturally
// following the version (npm's `resolved` names the tarball for it) is not
// evidence of anything.
func (c Change) PatchOrMinorEligible() bool {
	if len(c.Base) != 1 || len(c.Head) != 1 {
		return false
	}
	if c.Base[0] == c.Head[0] && c.BaseOrigin != c.HeadOrigin {
		return false
	}
	return PatchOrMinorEligible(c.Base[0], c.Head[0])
}

// PatchOrMinorEligible reports whether every version pair in the delta is
// eligible. A delta that changed no version is eligible, being a delta with
// nothing to disqualify it.
//
// Additions take no part in this. A change that adds a package is disqualified
// before any exemption is consulted, so folding the addition in here would
// state the same veto twice and in the weaker place.
//
// It is every pair and not most of them: a change patching one dependency and
// majoring another is not boring as a whole, and the major bump is not made
// boring by the patch sitting beside it in the same lockfile.
func (d Delta) PatchOrMinorEligible() bool {
	for _, c := range d.Changed {
		if !c.PatchOrMinorEligible() {
			return false
		}
	}
	return true
}
