package licence

import (
	"slices"
	"testing"
)

// The allow-list is the gate's whole on/off state, so an absent one must not
// answer for any dependency. Answering Conforms would render a repository that
// configured nothing as a repository whose dependencies all passed.
func TestEvaluateAnswersNotConfiguredWhileTheAllowListIsEmpty(t *testing.T) {
	for name, allow := range map[string][]string{"nil": nil, "empty": {}} {
		policy := NewPolicy(allow)
		if policy.Configured() {
			t.Errorf("%s: Configured = true, want false", name)
		}
		for _, d := range []Dependency{
			dep("cbindgen", "0.26.0", "MPL-2.0"),
			dep("libc", "0.2.0", "Apache-2.0 OR MIT"),
			dep("mystery", "1.0.0", Unknown),
		} {
			if got := policy.Evaluate(d); got != NotConfigured {
				t.Errorf("%s: %s = %q, want %q", name, d.Package, got, NotConfigured)
			}
		}
	}
}

// Conformance is decided over the expression, not over its text. `Apache-2.0
// OR MIT` is the dual-licence case an allow-list satisfies through either
// half, and a string comparison against the allow-list rejects it.
func TestEvaluateSatisfiesAnOrThroughEitherHalf(t *testing.T) {
	libc := dep("libc", "0.2.0", "Apache-2.0 OR MIT")
	for _, allow := range [][]string{{"MIT"}, {"Apache-2.0"}, {"MIT", "Apache-2.0"}} {
		if got := NewPolicy(allow).Evaluate(libc); got != Conforms {
			t.Errorf("allow=%v: %q = %q, want %q", allow, libc.Licence, got, Conforms)
		}
	}
	if got := NewPolicy([]string{"GPL-3.0-only"}).Evaluate(libc); got != Rejected {
		t.Errorf("an allow-list satisfying neither half = %q, want %q", got, Rejected)
	}
}

// An AND is every half or none of it. Read as an OR, a dependency that is MIT
// *and* GPL passes under an allow-list holding MIT alone — which is the
// copyleft obligation the gate exists to catch, admitted silently.
func TestEvaluateRequiresEveryHalfOfAnAnd(t *testing.T) {
	both := dep("bundled", "1.0.0", "MIT AND GPL-3.0-only")
	if got := NewPolicy([]string{"MIT"}).Evaluate(both); got != Rejected {
		t.Errorf("an allow-list holding one half = %q, want %q", got, Rejected)
	}
	if got := NewPolicy([]string{"MIT", "GPL-3.0-only"}).Evaluate(both); got != Conforms {
		t.Errorf("an allow-list holding both halves = %q, want %q", got, Conforms)
	}
}

// The ordinary two answers: an id the list holds, and one it omits.
func TestEvaluateAcceptsAnAllowedIdentifierAndRejectsAnOmittedOne(t *testing.T) {
	policy := NewPolicy([]string{"Apache-2.0", "BSD-3-Clause", "ISC", "MIT"})
	if got := policy.Evaluate(dep("serde", "1.0.0", "MIT")); got != Conforms {
		t.Errorf("an allowed identifier = %q, want %q", got, Conforms)
	}
	if got := policy.Evaluate(dep("cbindgen", "0.26.0", "MPL-2.0")); got != Rejected {
		t.Errorf("an omitted identifier = %q, want %q", got, Rejected)
	}
}

// Every way of failing to read a licence answers the same way, and none of
// them answers Conforms. `MIT OR (` is the case the SPDX parser panics on
// rather than returning an error, so a policy that only checks the error makes
// one unreadable manifest the end of the scan.
func TestEvaluateCallsAnUnreadableLicenceUnclassifiable(t *testing.T) {
	policy := NewPolicy([]string{"MIT"})
	for name, expression := range map[string]string{
		"none stated":            "",
		"already unknown":        Unknown,
		"not an identifier":      "NOT-A-LICENCE",
		"the slash form":         "MIT/Apache-2.0",
		"an unclosed bracket":    "MIT OR (",
		"an operator with no id": "MIT OR",
	} {
		got := policy.Evaluate(dep("mystery", "1.0.0", expression))
		if got != Unclassifiable {
			t.Errorf("%s (%q) = %q, want %q", name, expression, got, Unclassifiable)
		}
	}
}

// The set the gate compares is the non-conforming pairs and never a full
// inventory: a conforming dependency in it fails a row nothing introduced.
func TestRejectHoldsOnlyTheNonConformingPairs(t *testing.T) {
	policy := NewPolicy([]string{"Apache-2.0", "MIT"})
	set := policy.Reject([]Dependency{
		dep("libc", "0.2.0", "Apache-2.0 OR MIT"),
		dep("serde", "1.0.0", "MIT"),
		dep("cbindgen", "0.26.0", "MPL-2.0"),
		dep("juju-errors", "1.0.0", "LGPL-3.0-only"),
	})
	var got []string
	for _, d := range set.Dependencies() {
		got = append(got, d.Package)
	}
	want := []string{"cbindgen", "juju-errors"}
	if !slices.Equal(got, want) {
		t.Fatalf("rejected = %v, want %v", got, want)
	}
}

// An unreadable licence is a pair like any other, with `unknown` on the
// licence side. Dropped, the gate silently allows every dependency nobody
// could classify; kept under the text that failed to parse, two unreadable
// manifests become two pairs whose difference is a typo.
func TestRejectRecordsAnUnreadableLicenceAsUnknown(t *testing.T) {
	policy := NewPolicy([]string{"MIT"})
	set := policy.Reject([]Dependency{
		dep("mystery", "1.0.0", ""),
		dep("garbled", "2.0.0", "MIT OR ("),
	})
	if set.Len() != 2 {
		t.Fatalf("Len = %d, want 2 — an unreadable licence is not silently allowed", set.Len())
	}
	for _, pkg := range []string{"mystery", "garbled"} {
		if !set.Has(Pair{Package: pkg, Licence: Unknown}) {
			t.Errorf("%s is not held under %q: %v", pkg, Unknown, set.Dependencies())
		}
	}
}

// An unconfigured policy states nothing about any dependency, so it rejects
// none of them. Rejecting them all is cargo-deny's default and the reason this
// design refuses to invent one: a gate whose unconfigured behaviour fails
// every repository is a gate that gets switched off.
func TestRejectHoldsNothingWhileTheAllowListIsEmpty(t *testing.T) {
	set := NewPolicy(nil).Reject([]Dependency{
		dep("cbindgen", "0.26.0", "MPL-2.0"),
		dep("mystery", "1.0.0", Unknown),
	})
	if set.Len() != 0 {
		t.Fatalf("an unconfigured policy rejected %v", set.Dependencies())
	}
}

// The policy outlives the call that built it, and a set is read long after.
func TestNewPolicyCopiesTheAllowList(t *testing.T) {
	allow := []string{"MIT"}
	policy := NewPolicy(allow)
	allow[0] = "MPL-2.0"
	if got := policy.Evaluate(dep("serde", "1.0.0", "MIT")); got != Conforms {
		t.Fatalf("editing the caller's slice changed the policy: %q", got)
	}
	returned := policy.Allow()
	returned[0] = "GPL-3.0-only"
	if got := policy.Evaluate(dep("serde", "1.0.0", "MIT")); got != Conforms {
		t.Fatalf("editing the returned slice changed the policy: %q", got)
	}
}
