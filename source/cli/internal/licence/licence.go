// Package licence is the licence-compliance gate's language-neutral core:
// which licences a repository may ship, and which non-conforming dependencies
// a change introduces against its merge-base.
//
// It runs no tool and reads no manifest. Each language's scanner produces the
// Dependency values for its own ecosystem — cargo-deny's rejections for Rust,
// licensecheck over each module root for Go — and locates the resulting claim
// at the lockfile stanza or the require line naming the package, which only
// that scanner knows how to find. What every language shares, and what lives
// here, is the one stated policy they all read and the delta they are all
// gated on: a pair absent at the merge-base is the only thing that fails a
// row. See docs/adr/0038.
package licence

import (
	"cmp"
	"slices"
	"strings"
)

// Gate names the licence gate, on the row it reports and on every finding it
// produces.
const Gate = "licence"

// Unknown is the licence side of a claim about a dependency whose licence
// nobody could read: no licence file that classified, no licence field stated,
// or an expression no SPDX parser accepts.
//
// It is a pair like any other and grandfathers like any other, so adopting the
// gate does not fail a repository over a dependency whose licence was already
// unreadable. What it must never be is absent: a dependency dropped from the
// set because its licence could not be determined is a dependency the gate
// silently allowed.
const Unknown = "unknown"

// Dependency is one dependency and the licence it states.
type Dependency struct {
	// Package is the crate name or module path, as its own ecosystem spells
	// it. It is never qualified further: a component names one runner, which
	// implies one language, so a `serde` crate and a `serde` package can
	// never meet inside one set.
	Package string
	// Version locates the claim for a reader and takes no part in the gate's
	// key. Keyed on the version, every bump of an already-grandfathered
	// dependency would read as a pair nothing grandfathered and fail the row
	// — the gate firing on the ordinary maintenance it has no claim about.
	Version string
	// Licence is an SPDX expression as the ecosystem states it —
	// `Apache-2.0 OR MIT` — or Unknown. A dependency whose licence is read
	// from files rather than declared states it through Expression.
	Licence string
}

// Key is how the gate identifies this dependency: by package and licence.
func (d Dependency) Key() Pair { return Pair{Package: d.Package, Licence: d.Licence} }

// Pair is a non-conforming dependency as the gate keys it — package and
// licence, never version.
//
// A bump whose licence is unchanged is the pair that was already there; a bump
// that changes the licence is a pair nothing grandfathered, which is the case
// worth a human. A package removed and re-added in one change is likewise the
// pair that was already there: the change did not introduce it.
type Pair struct {
	Package string
	Licence string
}

// Site identifies a licence claim independently of the line it lands on, for
// finding.Finding.
//
// A claim is located at the manifest line naming the package, and the text of
// that line is deliberately not what identifies it: a module offering two
// licences produces two claims against one line, and a site read from the line
// would make them one and drop the second. So the site is the pair, stable
// across a lockfile reordering and across the bump that moves the line.
func (p Pair) Site() string { return Gate + "\x1f" + p.Package + " " + p.Licence }

// Expression is a set of detected licence ids as the SPDX expression they are
// read as: any one of them satisfying the policy conforms.
//
// Go states no licence anywhere a tool can read, so a module's licence is
// whatever the licence files at its root classify as, and a module offering
// several of them is read the way an expression's OR is read —
// `gopkg.in/yaml.v2` carries Apache-2.0 in LICENSE and MIT in LICENSE.libyaml,
// and an allow-list holding either passes it. The stated cost is a dependency
// that bundles a second, separately-licensed component under its own licence
// file rather than genuinely offering a choice: nothing in a flat set of files
// tells the two apart, and this reads both as a choice.
//
// The ids are sorted and deduplicated, so a module's licence does not depend
// on the order its files happened to be read in — which would otherwise put a
// directory walk into a finding's identity. Ids in, nothing classified out:
// an empty set answers Unknown, because there is no expression to evaluate.
func Expression(ids []string) string {
	seen := make(map[string]struct{}, len(ids))
	var unique []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return Unknown
	}
	slices.Sort(unique)
	return strings.Join(unique, " OR ")
}

// Set is one component's non-conforming pairs.
//
// It is only ever the non-conforming ones and never a full inventory. That is
// what makes recomputing the base side cheap — cargo-deny reports only the
// crates it rejected, so the set is what the tool already hands back — and it
// is what keeps the gate quiet: a policy edit that widens the allow-list
// shrinks both sides at once and introduces nothing.
//
// The zero value is the empty set and is usable.
type Set struct {
	pairs map[Pair]Dependency
}

// NewSet is the set holding deps.
func NewSet(deps ...Dependency) Set {
	var s Set
	for _, d := range deps {
		s.Add(d)
	}
	return s
}

// Add puts d in the set, keyed by its pair.
//
// The first dependency under a pair is kept, so the version a claim reports is
// the first the scanner named rather than whichever came last.
func (s *Set) Add(d Dependency) {
	if s.pairs == nil {
		s.pairs = make(map[Pair]Dependency)
	}
	if _, held := s.pairs[d.Key()]; held {
		return
	}
	s.pairs[d.Key()] = d
}

// Len is how many pairs the set holds.
func (s Set) Len() int { return len(s.pairs) }

// Has reports whether the set holds p.
func (s Set) Has(p Pair) bool {
	_, held := s.pairs[p]
	return held
}

// Dependencies is every pair in the set, ordered by package and then licence.
//
// The order is total and derived from the pairs themselves, so two runs over
// one tree report the claims in one order however the map iterated.
func (s Set) Dependencies() []Dependency {
	out := make([]Dependency, 0, len(s.pairs))
	for _, d := range s.pairs {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Dependency) int {
		if c := cmp.Compare(a.Package, b.Package); c != 0 {
			return c
		}
		return cmp.Compare(a.Licence, b.Licence)
	})
	return out
}

// Without is every pair in s that other does not hold: what a change
// introduced, given the base set it is compared against.
func (s Set) Without(other Set) Set {
	var out Set
	for p, d := range s.pairs {
		if other.Has(p) {
			continue
		}
		out.Add(d)
	}
	return out
}
