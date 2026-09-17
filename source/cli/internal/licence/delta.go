package licence

// BaseState is what became of the merge-base side of the comparison.
type BaseState string

const (
	// NoBase is that no base was asked for: `lydite scan` on `main`, with no
	// diff base to compare against. It is the zero value, so a Base nobody
	// filled in gates nothing rather than comparing against an empty set —
	// which would report every non-conforming dependency in the repository as
	// introduced by whatever change happened to be running.
	NoBase BaseState = ""
	// Measured is a base tree that was checked out and whose set was read.
	Measured BaseState = "measured"
	// Unmeasured is a base that was asked for and could not be built: a
	// worktree that would not check out, a module download that would not
	// resolve, a tool that would not run. It gates nothing and says why. It
	// is never folded into Measured with an empty set, because a gate that
	// could not run must not render as one that ran and found nothing.
	Unmeasured BaseState = "unmeasured"
)

// Base is the merge-base side of the comparison, with what became of it.
//
// The set is recomputed at the merge-base rather than read from a stored
// baseline: an entry written by a lydite that did not yet compute licences
// reads back as the empty set, so the delta on the day of the upgrade is the
// absolute set and fails every adopting repository over dependencies nobody in
// that change chose.
//
// The zero value is NoBase.
type Base struct {
	state  BaseState
	set    Set
	reason string
}

// NoDiffBase is the base of a run that was given none.
func NoDiffBase() Base { return Base{state: NoBase} }

// MeasuredBase is the set read at the merge-base.
func MeasuredBase(set Set) Base { return Base{state: Measured, set: set} }

// UnmeasuredBase is a base that could not be built, with what failed. The
// reason is rendered on the row, so it names the step rather than restating
// that something went wrong.
func UnmeasuredBase(reason string) Base { return Base{state: Unmeasured, reason: reason} }

// State is what became of the base.
func (b Base) State() BaseState { return b.state }

// Set is the base's non-conforming pairs, empty unless the base was measured.
func (b Base) Set() Set { return b.set }

// Reason is what failed, empty unless the base is Unmeasured.
func (b Base) Reason() string { return b.reason }

// Verdict is what the licence gate says about one component.
type Verdict string

const (
	// VerdictNotConfigured is the repository having stated no policy. It is
	// the zero value, so a Comparison nobody produced reads as a gate that
	// did not run.
	VerdictNotConfigured Verdict = ""
	// VerdictPass is a change that introduced no non-conforming pair.
	VerdictPass Verdict = "pass"
	// VerdictFail is a change that introduced one, which is the whole of what
	// the gate exists to catch.
	VerdictFail Verdict = "fail"
	// VerdictUnmeasured is a base that could not be built. It gates nothing
	// and reports the current set as context, so a reader sees what the gate
	// would have compared.
	VerdictUnmeasured Verdict = "unmeasured"
	// VerdictContext is a run with no diff base at all. It reports the full
	// non-conforming set and gates nothing, the shape a scan with no
	// --diff-base already has.
	VerdictContext Verdict = "context"
)

// Gating reports whether this verdict can fail a row. A verdict that gates
// nothing must not be read as one that ran and found nothing.
func (v Verdict) Gating() bool { return v == VerdictPass || v == VerdictFail }

// Comparison is the gate's whole answer about one component.
type Comparison struct {
	// Verdict is what the row reports.
	Verdict Verdict
	// Pairs is what the verdict is about, ordered: the introduced pairs under
	// VerdictFail, the whole current set under VerdictContext and
	// VerdictUnmeasured, and nothing under the two verdicts that have nothing
	// to name.
	Pairs []Dependency
	// Reason is what stopped the base being built, empty under every other
	// verdict.
	Reason string
}

// Compare is the gate's answer for one component: the current non-conforming
// set against the base, under the stated policy.
//
// The policy is read here and not only where the current set was built, so
// that an unconfigured repository cannot reach a passing verdict through an
// empty current set. Two empty sets and no policy are indistinguishable from
// two empty sets and a policy nothing violated, and only one of those is a
// gate that ran.
func Compare(policy Policy, current Set, base Base) Comparison {
	if !policy.Configured() {
		return Comparison{Verdict: VerdictNotConfigured}
	}
	switch base.State() {
	case NoBase:
		return Comparison{Verdict: VerdictContext, Pairs: current.Dependencies()}
	case Measured:
		introduced := current.Without(base.Set())
		if introduced.Len() == 0 {
			return Comparison{Verdict: VerdictPass}
		}
		return Comparison{Verdict: VerdictFail, Pairs: introduced.Dependencies()}
	case Unmeasured:
	}
	// Unmeasured, and equally a state this does not recognise. Falling here
	// rather than to a verdict is the safe direction: an unrecognised base is
	// one nothing can be compared against, and the row must say so rather than
	// compare against the empty set it happens to hold.
	reason := base.Reason()
	if reason == "" {
		reason = "the base licence set could not be read"
	}
	return Comparison{Verdict: VerdictUnmeasured, Pairs: current.Dependencies(), Reason: reason}
}
