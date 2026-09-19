package depdelta

import "slices"

// Change is one package present at both sides of a comparison, pinned at
// different versions.
type Change struct {
	// Name is the package, as its own ecosystem spells it.
	Name string
	// Base is every version the merge-base pinned it at, sorted.
	Base []string
	// Head is every version HEAD pins it at, sorted.
	Head []string
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
		if !slices.Equal(b, h) {
			d.Changed = append(d.Changed, Change{Name: name, Base: b, Head: h})
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
func (c Change) PatchOrMinorEligible() bool {
	if len(c.Base) != 1 || len(c.Head) != 1 {
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
