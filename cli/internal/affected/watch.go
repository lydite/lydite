package affected

import (
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/pathmatch"
)

// Unmatched is a watch pattern that covers no file in the tree.
type Unmatched struct {
	Component string
	Pattern   string
}

// UnmatchedWatch reports every declared watch pattern matching none of files.
//
// This is a gate, and deliberately a fatal one where an unmatched exclude is
// only named on stderr. The two look identical — a pattern in
// .lydite/components.yml covering nothing — and their consequences are
// opposite. An exclude covering nothing excludes nothing, so the orphan gate
// stays stricter than declared and the failure is on the safe side; a watch
// covering nothing means the component stops running when its input changes,
// which is invisible, permanent, and green on every run. Treating both the
// same way would be a false symmetry.
//
// The tree is the authority rather than the pattern's syntax, because the
// dangerous typo is syntactically perfect: "Makefil" is a valid pattern, and
// so is a bare "docs" written where "docs/**" was meant — the first matches
// nothing and the second matches only a file of that exact name, since these
// patterns are anchored and do not float.
//
// Files must be every path git knows about, not only source: a watch
// legitimately names a Makefile, a VERSION file or an OpenAPI document, none
// of which any component could ever claim.
//
// Order is declaration order, then the order the patterns were written, so two
// runs over one declaration produce the same rows.
func UnmatchedWatch(f component.File, files []string) []Unmatched {
	var out []Unmatched
	for _, c := range f.Components {
		for _, w := range c.Watch {
			if !matchesFile(w, files) {
				out = append(out, Unmatched{Component: c.Name, Pattern: w})
			}
		}
	}
	return out
}

func matchesFile(pattern string, files []string) bool {
	for _, f := range files {
		if pathmatch.Match(pattern, f) {
			return true
		}
	}
	return false
}
