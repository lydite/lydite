// Package depdelta is what a change did to a repository's dependencies: which
// packages it added, which it removed, and how far the versions of the ones it
// kept moved.
//
// It answers two different questions with one comparison. A package present at
// HEAD and absent at the merge-base is new code entering the tree, direct or
// transitive, and nothing a version string says makes it less new. A package
// present at both sides moved by a patch, a minor, a major, or by an amount
// this cannot say at all — which is the only input a condition on routine
// version maintenance can be built out of. See
// docs/adr/0047-an-added-dependency-refers-and-a-version-bump-is-conditionally-exempt.md.
//
// Every reader here takes bytes and a label. The base side of a comparison is
// `git show <merge-base>:<path>`, so nothing in this package runs git, reads a
// worktree, or knows where a manifest lives — a caller that read the content
// some other way gets the same answer.
//
// Three ecosystems have readers, and the rest are named rather than ignored.
// Detect says which manifest a path is and Manifest.Readable says whether a
// reader exists for it, so a caller can tell a yarn lockfile nothing here can
// parse from a path that is no manifest at all. The two must reach different
// verdicts: a set this could not build says nothing about whether the change
// added a dependency, and reporting that as "it added none" is the one failure
// direction the whole comparison exists to avoid.
package depdelta

import (
	"fmt"
	"slices"

	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/typescript"
)

// Set is the packages one manifest pins, each with the versions pinned for
// it, and the origin recorded for each (name, version) pin where the
// ecosystem names one.
//
// A name carries a list rather than a version because a manifest legitimately
// pins two of one package — a cargo graph resolving `rand` at 0.8 and 0.9, an
// npm tree nesting a duplicate, a `replace` disagreeing with a `require`. The
// zero value is the empty set.
type Set struct {
	versions map[string][]string
	origins  map[[2]string]string
}

// NewSet is the set holding the versions each named package is pinned at,
// with no origin recorded for any of them — the shape a manifest with no
// origin signal of its own produces (`go.mod`, which pins no content hash).
// Each name's versions are sorted and deduplicated, so two readings of one
// manifest can never differ by the order a map happened to be ranged in.
func NewSet(versions map[string][]string) Set {
	return NewSetWithOrigins(versions, nil)
}

// NewSetWithOrigins is NewSet, additionally recording the origin — Cargo's
// `source`, npm's `resolved`, a go.sum content hash — each (name, version)
// pin names. A pair origins has nothing for is the empty string, the same
// answer NewSet gives every pair.
func NewSetWithOrigins(versions map[string][]string, origins map[[2]string]string) Set {
	s := Set{versions: make(map[string][]string, len(versions)), origins: map[[2]string]string{}}
	for name, vs := range versions {
		sorted := slices.Clone(vs)
		slices.Sort(sorted)
		sorted = slices.Compact(sorted)
		s.versions[name] = sorted
		for _, v := range sorted {
			if o := origins[[2]string{name, v}]; o != "" {
				s.origins[[2]string{name, v}] = o
			}
		}
	}
	return s
}

// Len is how many distinct packages the set holds.
func (s Set) Len() int { return len(s.versions) }

// Names is every package in the set, sorted.
func (s Set) Names() []string {
	names := make([]string, 0, len(s.versions))
	for name := range s.versions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Has reports whether the set pins the package at all.
func (s Set) Has(name string) bool {
	_, ok := s.versions[name]
	return ok
}

// Versions is every version the set pins the package at, sorted, and empty
// when it does not pin it.
func (s Set) Versions(name string) []string { return slices.Clone(s.versions[name]) }

// Origin is the origin recorded for one (name, version) pin — Cargo's
// `source`, npm's `resolved`, a go.sum content hash — and empty when the
// ecosystem names none, or when the set does not pin that pair at all.
func (s Set) Origin(name, version string) string { return s.origins[[2]string{name, version}] }

// Extract is the dependency set a manifest's content states.
//
// A manifest with no reader is an error rather than an empty set, and so is one
// whose content does not parse. Both are states in which lydite does not know
// what the change did to the dependencies, and an empty set is the claim that
// it knows the change did nothing.
//
// source labels the content for the error message — which manifest, and which
// side of the comparison — because a caller holds two of these at once and an
// error naming neither is one nobody can act on.
func Extract(m Manifest, content []byte, source string) (Set, error) {
	switch m {
	case ManifestGoMod:
		return NewSet(golang.GoModDependencies(content)), nil
	case ManifestGoSum:
		versions, origins, err := golang.GoSumDependencies(content)
		if err != nil {
			return Set{}, fmt.Errorf("%s: %w", source, err)
		}
		return NewSetWithOrigins(versions, origins), nil
	case ManifestCargoLock:
		versions, origins := rust.LockDependencies(content)
		return NewSetWithOrigins(versions, origins), nil
	case ManifestNPMLock:
		versions, origins, err := typescript.LockDependencies(content)
		if err != nil {
			return Set{}, fmt.Errorf("%s: %w", source, err)
		}
		return NewSetWithOrigins(versions, origins), nil
	}
	if m == ManifestNone {
		return Set{}, fmt.Errorf("%s: names no dependency manifest", source)
	}
	return Set{}, fmt.Errorf("%s: no dependency reader for %s", source, m.Ecosystem())
}
