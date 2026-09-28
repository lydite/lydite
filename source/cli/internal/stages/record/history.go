package recordstages

import (
	"context"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/ledger"
)

// ComposeLedgerInputsIn is every scalar a recording carries, and the crashes
// its scan named.
type ComposeLedgerInputsIn struct {
	// Folded is FoldMeasurementsOut.Folded.
	Folded Measurements
	// PerComponent and Root are CountFindingsOut's.
	PerComponent map[string]map[string]int
	Root         map[string]int
	// Crashed is ReadReportsOut.Crashed.
	Crashed []finding.Crash
	// Mutants is BindMutantsOut.Components.
	Mutants map[string]MutantCounts
}

// ComposeLedgerInputsOut is what this recording measured, in the ledger's own
// vocabulary.
type ComposeLedgerInputsOut struct {
	// Components is each component's scalars as the ledger records them. A
	// component holding none is absent.
	Components map[string]ledger.Component
	// RootFindings is the per-gate finding counts taken over the repository
	// rather than any one component.
	RootFindings map[string]int
	// Scope is every finding bucket this recording measured.
	Scope map[ledger.FindingBucket]bool
}

// ComposeLedgerInputs is what this recording measured and which finding
// buckets it measured, as the values a quality history is composed from.
//
// Both are record's to decide, because both are read off documents only a
// recording folds: the measurements, the counts and the mutants a component's
// scalars come from, and the crashes a scan named, which decide that a bucket
// counted 0 was not measured at all. Composing a history out of them, and
// against which branch, is not this stage's to do.
func ComposeLedgerInputs(_ context.Context, in ComposeLedgerInputsIn) (ComposeLedgerInputsOut, error) {
	return ComposeLedgerInputsOut{
		Components:   historyComponents(in.Folded, in.PerComponent, in.Mutants),
		RootFindings: in.Root,
		Scope:        findingScope(in.PerComponent, in.Root, in.Crashed),
	}, nil
}

// findingScope is every bucket this recording measured: each gate
// FindingCounts keyed for a component, and each root-scoped one it keyed for
// the repository, less every bucket the scan named as crashed.
//
// Read off the counts rather than decided again, because a bucket a finding
// can resolve in is a gate that applied, and the counts are already the one
// answer to that. A second notion of what applied would drift from the first,
// and the drift would be a resolution recorded for a bucket nothing measured.
//
// The counts cannot say whether an applicable gate finished: a scanner that
// crashed found no claims and counts 0 like a clean one, which is a limit a
// scalar tolerates and a transition does not — every finding held open there
// would be recorded resolved, and appear again on the next clean run, in a
// ledger nothing is ever removed from. The scan document names each crash as
// data, and a crashed bucket is left out so the open set it holds carries over
// untouched.
func findingScope(perComponent map[string]map[string]int, root map[string]int, crashed []finding.Crash) map[ledger.FindingBucket]bool {
	scope := map[ledger.FindingBucket]bool{}
	for name, gates := range perComponent {
		for gate := range gates {
			scope[ledger.FindingBucket{Gate: gate, Component: name}] = true
		}
	}
	for gate := range root {
		scope[ledger.FindingBucket{Gate: gate}] = true
	}
	for _, c := range crashed {
		delete(scope, ledger.FindingBucket{Gate: c.Gate, Component: c.Component})
	}
	return scope
}

// historyComponents is each component's scalars as the ledger records them.
//
// The counts are what was MEASURED and never the anchored ones a baseline
// records: anchoring exists so a within-tolerance dip does not ratchet the
// gate downwards, which is a property of the gate rather than of the commit,
// and a history that recorded it would draw a line nothing ever measured.
//
// A component this run carried rather than measured keeps its number, because
// affected selection established that the change could not have touched it —
// so a flat line is the true one, and dropping it would make the series vanish
// and reappear with whatever a change happened to touch.
//
// The per-gate finding counts come in from the side, because they are measured
// by `lydite scan` and not by the run that wrote these measurements.
//
// The mutant counts come in from the side for the same reason, out of a
// `lydite mutation` run, and are empty for every recording that read none. A
// component absent from them is a component nothing mutated — untouched by the
// diff, declared `mutation: false`, or a run that did not complete — and
// records no mutation at all, because a zeroed one reads as a suite that killed
// everything and nothing later corrects it.
func historyComponents(folded Measurements, perComponent map[string]map[string]int,
	mutants map[string]MutantCounts) map[string]ledger.Component {
	out := map[string]ledger.Component{}
	for name, m := range folded.Components {
		c := ledger.Component{Producer: m.Producer}
		lines := m.LineCount
		if m.Unanchored != nil {
			lines = *m.Unanchored
		}
		if lines.Measured() {
			c.Coverage = &ledger.Lines{Covered: lines.Covered, Total: lines.Total}
		}
		if m.CRAP != nil {
			c.CRAP = &ledger.CRAP{Above: m.CRAP.Above, Worst: m.CRAP.Worst}
		}
		out[name] = c
	}
	for name, counts := range folded.Tests {
		c := out[name]
		c.Tests = &counts
		out[name] = c
	}
	// The finding counts reach a component the measurements never mention, and
	// that is the point: a scan covers every declared component whatever the
	// suites did, so a component whose tests were carried still has a finding
	// series.
	for name, gates := range perComponent {
		c := out[name]
		c.Findings = gates
		out[name] = c
	}
	// And the mutant counts reach one the measurements never mention for the
	// same reason: a component the merge commit's diff touched is mutated
	// whatever its suite was carried or measured by.
	for name, counts := range mutants {
		c := out[name]
		c.Mutation = &ledger.Mutation{
			Killed:         counts.Killed,
			TimedOut:       counts.TimedOut,
			OutOfMemory:    counts.OutOfMemory,
			Survived:       counts.Survived,
			Unviable:       counts.Unviable,
			Acknowledged:   counts.Acknowledged,
			ElapsedSeconds: counts.ElapsedSeconds,
		}
		out[name] = c
	}
	// A component holding no scalar at all contributes nothing but its name,
	// which is a point on no line.
	for name, c := range out {
		if c.Coverage == nil && c.CRAP == nil && c.Tests == nil && c.Findings == nil && c.Mutation == nil {
			delete(out, name)
		}
	}
	return out
}
