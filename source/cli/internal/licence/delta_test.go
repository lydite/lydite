package licence

import (
	"slices"
	"testing"
)

func packages(deps []Dependency) []string {
	var out []string
	for _, d := range deps {
		out = append(out, d.Package+" "+d.Licence)
	}
	return out
}

// The delta is what the change introduced, which is the set difference and not
// a count: a change that removes one copyleft dependency and adds another
// leaves the count where it was, and that is the whole of what the gate exists
// to catch.
func TestWithoutHoldsOnlyThePairsTheBaseLacks(t *testing.T) {
	base := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	current := NewSet(dep("juju-errors", "1.0.0", "LGPL-3.0-only"))
	introduced := current.Without(base)
	if got, want := packages(introduced.Dependencies()), []string{"juju-errors LGPL-3.0-only"}; !slices.Equal(got, want) {
		t.Fatalf("introduced = %v, want %v", got, want)
	}
}

// A bump whose licence is unchanged is the pair that was already there. Keyed
// on the version, the gate fires on the ordinary maintenance it has no claim
// about.
func TestABumpOfAGrandfatheredPairIntroducesNothing(t *testing.T) {
	base := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	current := NewSet(dep("cbindgen", "0.27.0", "MPL-2.0"))
	if got := current.Without(base); got.Len() != 0 {
		t.Fatalf("a bump introduced %v", packages(got.Dependencies()))
	}
}

// A bump that changes the licence is a pair nothing grandfathered, which is
// exactly the case worth a human. Keyed on the package alone, it disappears.
func TestALicenceChangeOnAGrandfatheredPackageIsIntroduced(t *testing.T) {
	base := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	current := NewSet(dep("cbindgen", "0.27.0", "GPL-3.0-only"))
	got := current.Without(base)
	if want := []string{"cbindgen GPL-3.0-only"}; !slices.Equal(packages(got.Dependencies()), want) {
		t.Fatalf("introduced = %v, want %v", packages(got.Dependencies()), want)
	}
}

// The difference is one-directional. A dependency the change removed is
// present at the base and absent now, and reporting it would fail the change
// that cleared it.
func TestAPairOnlyTheBaseHoldsIsNotIntroduced(t *testing.T) {
	base := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"), dep("juju-errors", "1.0.0", "LGPL-3.0-only"))
	current := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	if got := current.Without(base); got.Len() != 0 {
		t.Fatalf("removing a dependency introduced %v", packages(got.Dependencies()))
	}
}

// A repository that stated no policy has no gate, whatever the sets hold. A
// pass here is a row rendering green for a check nobody configured.
func TestCompareReportsNotConfiguredWithoutAPolicy(t *testing.T) {
	current := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	got := Compare(NewPolicy(nil), current, MeasuredBase(Set{}))
	if got.Verdict != VerdictNotConfigured {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictNotConfigured)
	}
	if got.Verdict.Gating() {
		t.Error("an unconfigured policy produced a gating verdict")
	}
	if len(got.Pairs) != 0 {
		t.Errorf("Pairs = %v, want none", packages(got.Pairs))
	}
}

// A scan with no diff base has nothing to grandfather against, so it reports
// the whole non-conforming set and gates nothing. Compared against the empty
// set instead, every dependency in the repository reads as introduced by
// whichever change happened to be running.
func TestCompareReportsContextWithNoDiffBase(t *testing.T) {
	current := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"), dep("juju-errors", "1.0.0", "LGPL-3.0-only"))
	got := Compare(NewPolicy([]string{"MIT"}), current, NoDiffBase())
	if got.Verdict != VerdictContext {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictContext)
	}
	if got.Verdict.Gating() {
		t.Error("a run with no diff base produced a gating verdict")
	}
	want := []string{"cbindgen MPL-2.0", "juju-errors LGPL-3.0-only"}
	if !slices.Equal(packages(got.Pairs), want) {
		t.Errorf("Pairs = %v, want the whole current set %v", packages(got.Pairs), want)
	}
}

// A gate that could not run never renders as one that passed. The base side is
// a worktree checkout and a tool invocation, and both can fail.
func TestCompareReportsUnmeasuredWhenTheBaseCouldNotBeBuilt(t *testing.T) {
	current := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	reason := "the base worktree would not check out"
	got := Compare(NewPolicy([]string{"MIT"}), current, UnmeasuredBase(reason))
	if got.Verdict != VerdictUnmeasured {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictUnmeasured)
	}
	if got.Verdict.Gating() {
		t.Error("an unmeasured base produced a gating verdict")
	}
	if got.Reason != reason {
		t.Errorf("Reason = %q, want %q", got.Reason, reason)
	}
	if want := []string{"cbindgen MPL-2.0"}; !slices.Equal(packages(got.Pairs), want) {
		t.Errorf("Pairs = %v, want the current set %v", packages(got.Pairs), want)
	}
}

// An unmeasured row names what failed. A caller that had nothing to say still
// produces a row that says why, rather than a status with no explanation
// beside it.
func TestCompareUnmeasuredAlwaysNamesAReason(t *testing.T) {
	got := Compare(NewPolicy([]string{"MIT"}), Set{}, UnmeasuredBase(""))
	if got.Verdict != VerdictUnmeasured {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictUnmeasured)
	}
	if got.Reason == "" {
		t.Error("an unmeasured verdict carries no reason")
	}
}

// Grandfathering is the whole design: a policy applied absolutely fails every
// adopting repository on the licences it already ships.
func TestComparePassesWhenTheChangeIntroducedNothing(t *testing.T) {
	grandfathered := dep("cbindgen", "0.26.0", "MPL-2.0")
	got := Compare(NewPolicy([]string{"MIT"}), NewSet(grandfathered), MeasuredBase(NewSet(grandfathered)))
	if got.Verdict != VerdictPass {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictPass)
	}
	if !got.Verdict.Gating() {
		t.Error("a measured comparison produced a verdict that gates nothing")
	}
	if len(got.Pairs) != 0 {
		t.Errorf("Pairs = %v, want none", packages(got.Pairs))
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty", got.Reason)
	}
}

// Introducing a non-conforming pair fails the row, and the row names the pair
// rather than the whole set — the rest is debt the change did not add.
func TestCompareFailsOnAnIntroducedPair(t *testing.T) {
	grandfathered := dep("cbindgen", "0.26.0", "MPL-2.0")
	current := NewSet(grandfathered, dep("juju-errors", "1.0.0", "LGPL-3.0-only"))
	got := Compare(NewPolicy([]string{"MIT"}), current, MeasuredBase(NewSet(grandfathered)))
	if got.Verdict != VerdictFail {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictFail)
	}
	if want := []string{"juju-errors LGPL-3.0-only"}; !slices.Equal(packages(got.Pairs), want) {
		t.Errorf("Pairs = %v, want the introduced pair alone %v", packages(got.Pairs), want)
	}
}

// Only a comparison against a base that was measured can fail a row. A caller
// reading any other verdict as a result is the failure the statuses exist to
// prevent.
func TestOnlyPassAndFailGate(t *testing.T) {
	gating := map[Verdict]bool{
		VerdictPass:          true,
		VerdictFail:          true,
		VerdictNotConfigured: false,
		VerdictUnmeasured:    false,
		VerdictContext:       false,
	}
	for verdict, want := range gating {
		if got := verdict.Gating(); got != want {
			t.Errorf("%q.Gating() = %v, want %v", verdict, got, want)
		}
	}
}

// The zero values fail towards a gate that did not run: a Comparison nobody
// produced and a Base nobody filled in must not read as a pass or as an
// empty base set.
func TestTheZeroComparisonAndBaseGateNothing(t *testing.T) {
	var comparison Comparison
	if comparison.Verdict != VerdictNotConfigured {
		t.Errorf("the zero Comparison = %q, want %q", comparison.Verdict, VerdictNotConfigured)
	}
	if comparison.Verdict.Gating() {
		t.Error("the zero Comparison gates")
	}
	var base Base
	if base.State() != NoBase {
		t.Errorf("the zero Base = %q, want %q", base.State(), NoBase)
	}
	current := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	if got := Compare(NewPolicy([]string{"MIT"}), current, base); got.Verdict != VerdictContext {
		t.Errorf("the zero Base compared as %q, want %q", got.Verdict, VerdictContext)
	}
}
