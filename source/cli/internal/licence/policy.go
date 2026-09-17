package licence

import (
	"fmt"
	"slices"

	"github.com/github/go-spdx/v2/spdxexp"
)

// Conformance is what the policy says about one dependency.
type Conformance string

const (
	// NotConfigured is every dependency's answer while the allow-list is
	// empty, and it is the zero value on purpose: a caller holding a
	// Conformance it never obtained from Policy.Evaluate must not be holding
	// one that reads as conformance. An absent policy states nothing about
	// any dependency, so a row it reaches is `context` naming what is
	// missing, never `pass`.
	NotConfigured Conformance = ""
	// Conforms is an expression the allow-list satisfies.
	Conforms Conformance = "conforms"
	// Rejected is a licence the policy parsed and does not allow.
	Rejected Conformance = "rejected"
	// Unclassifiable is a dependency with no licence to evaluate: none
	// stated, or an expression no SPDX parser accepts. It is non-conforming
	// and enters the set with Unknown on the licence side, because a
	// dependency whose licence nobody could read is exactly the one a policy
	// cannot vouch for.
	Unclassifiable Conformance = "unclassifiable"
)

// Policy is the SPDX allow-list a repository states — the licences this
// organisation may ship — read from config.Licence.Policy.Allow.
//
// A deny-list was rejected for the design's central reason: it admits,
// silently, every licence nobody thought to name, and over a dependency graph
// nobody reads that is most of them.
type Policy struct {
	allow []string
}

// NewPolicy is the policy over allow, which is copied so that a later edit to
// the caller's slice cannot change what a set was evaluated against.
func NewPolicy(allow []string) Policy { return Policy{allow: slices.Clone(allow)} }

// Configured reports whether the repository stated a policy at all.
//
// The list carries the section's whole on/off state: a non-empty allow-list is
// configured and an absent or empty one is not. There is no separate enabled
// key, because a second switch that can disagree with the list is a state
// every reader has to resolve anyway.
func (p Policy) Configured() bool { return len(p.allow) > 0 }

// Allow is the stated identifiers, copied.
func (p Policy) Allow() []string { return slices.Clone(p.allow) }

// Evaluate is what the policy says about one dependency.
//
// Conformance is decided over the licence *expression* rather than over its
// text: `Apache-2.0 OR MIT` conforms under an allow-list holding either half,
// and `MIT AND GPL-3.0-only` conforms only under one holding both. Both are
// the SPDX expression semantics, and spdxexp decides them rather than a second
// implementation here — an OR/AND parser written twice agrees until one of
// them learns about `GPL-2.0+` or a `WITH` exception.
func (p Policy) Evaluate(d Dependency) Conformance {
	if !p.Configured() {
		return NotConfigured
	}
	if d.Licence == "" || d.Licence == Unknown {
		return Unclassifiable
	}
	ok, err := satisfies(d.Licence, p.allow)
	if err != nil {
		return Unclassifiable
	}
	if ok {
		return Conforms
	}
	return Rejected
}

// Reject is the non-conforming pairs among deps, which is the set the gate
// compares — never a full inventory.
//
// A dependency whose licence could not be read enters the set under Unknown
// rather than under the text that failed to parse, so that two unreadable
// manifests do not become two distinct pairs whose difference is a typo.
func (p Policy) Reject(deps []Dependency) Set {
	var out Set
	for _, d := range deps {
		switch p.Evaluate(d) {
		case Rejected:
			out.Add(d)
		case Unclassifiable:
			d.Licence = Unknown
			out.Add(d)
		case NotConfigured, Conforms:
		}
	}
	return out
}

// satisfies is spdxexp.Satisfies with a malformed expression contained.
//
// An expression reaches here as a dependency author's free text out of a
// manifest lydite does not own, and the parser does not always answer a
// malformed one with an error: an unclosed parenthesis with nothing after it
// — `MIT OR (` — dereferences a nil token and panics. An unreadable
// expression must be one dependency's `unknown`, not the end of the scan, so
// every way of failing to read one answers the same way.
func satisfies(expression string, allow []string) (ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			ok, err = false, fmt.Errorf("unreadable licence expression %q: %v", expression, r)
		}
	}()
	return spdxexp.Satisfies(expression, allow)
}
