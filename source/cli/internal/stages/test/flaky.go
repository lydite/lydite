package teststages

import (
	"context"
	"errors"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/finding"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// PrepareFlakyGateIn is what the gate needs resolved before any component
// starts.
type PrepareFlakyGateIn struct {
	Dir        string
	BaseBranch string
	// Requested is `--gate-flaky`.
	Requested bool
}

// PrepareFlakyGateOut is the gate the run carries out.
type PrepareFlakyGateOut struct {
	Gate *testrun.FlakyGate
}

// PrepareFlakyGate resolves the merge-base and the paths the change touched,
// once for the run rather than once per component: they are facts about the
// change, and asking git for them per component pays the same walk again for
// every one of them.
//
// A gate nobody asked for is still built. Its report is a row per component
// saying it was not gated, because a section that quietly disappears is
// indistinguishable from a concern that passed.
func PrepareFlakyGate(ctx context.Context, in PrepareFlakyGateIn) (PrepareFlakyGateOut, error) {
	return PrepareFlakyGateOut{Gate: testrun.NewFlakyGate(ctx, in.Dir, in.BaseBranch, in.Requested)}, nil
}

// FlakyGateIn is the gate as the run left it, and the set it reports on.
type FlakyGateIn struct {
	// Gate is Run's own Gate: the one each component's run examined its new
	// tests through, holding what every examination established.
	Gate *testrun.FlakyGate
	Own  []component.Component
}

// FlakyGateOut is the gate's section of the report.
type FlakyGateOut struct {
	// Rows is one flaky(<name>) row per component in Own, in declaration
	// order.
	Rows []ui.Row
	// Findings is one claim per new test whose two outcomes disagreed.
	Findings []finding.Finding
}

// errNoGate is a flaky gate that was never handed over: every run builds one,
// requested or not, so there is no report without one to give.
var errNoGate = errors.New("no flaky gate to report on")

// FlakyGate reports what the gate established.
//
// The examination itself is not done here. Each component's new tests are
// rerun from inside that component's own run, before its services come down —
// a service-dependent test rerun after teardown fails because nothing is
// listening, and would report the most reliable test in the repository as
// flaky — so Run carries the gate out and this stage renders it. The two are
// separate stages so the report has one section per stage; Run's Gate is
// handed here unchanged, and Run's rows followed by these are exactly what
// the one pass that ran and reported both produced (ADR 0062).
//
// A component whose suite never ran still takes a row: its new tests were not
// examined, and a gate that could not run never renders as one that passed.
func FlakyGate(_ context.Context, in FlakyGateIn) (FlakyGateOut, error) {
	if in.Gate == nil {
		return FlakyGateOut{}, errNoGate
	}
	rows, found := in.Gate.Report(in.Own)
	return FlakyGateOut{Rows: rows, Findings: found}, nil
}
