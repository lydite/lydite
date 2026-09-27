package measure

import (
	"fmt"
	"io"
	"math"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
)

// CRAPRecord is what this run would record as the CRAP baseline.
//
// A score rides on a coverage entry and never travels alone: it is computed
// from the coverage report, so an entry with no counts has nothing to hang one
// on — and `lydite test record`'s completeness check asks only whether a
// component has an entry, so a CRAP-only entry would satisfy it for a
// component whose coverage nobody measured.
//
// The carry rule is coverage's, asked again rather than assumed: a component
// this run did not select is unchanged from the tree the baseline describes,
// so its score is still that score, while one that ran and failed may be
// exactly what changed. A component whose coverage carried and which the CRAP
// baseline has no entry for carries nothing and reports `new` on the next
// change, which heals on the run after.
func CRAPRecord(ms []Measurement, record gitstate.Baseline, carried map[string]bool, anchor gitstate.CRAPBaseline) gitstate.CRAPBaseline {
	out := gitstate.CRAPBaseline{}
	for _, m := range ms {
		if _, ok := record[m.Name]; !ok {
			continue
		}
		if m.Scored() {
			out[m.Name] = m.CRAPEntry()
			continue
		}
		// A component that ran and could not be scored records nothing. Its
		// content may be exactly what changed, so the baseline's entry is a
		// guess — and the next change reports it `new`, gates nothing for that
		// one change, and records it, which heals rather than persisting.
		if e, ok := carriedScore(m, carried, anchor); ok {
			out[m.Name] = e
		}
	}
	return out
}

// Recordable counts the declared components a complete baseline must cover:
// every one except those nothing could ever measure, which contribute to
// neither side of any comparison and whose absence is permanent and expected.
//
// It reads the declaration alone, exactly as `lydite test record` does, so the
// row a run shows and the question the fold asks are the same question.
func Recordable(decl component.File) int {
	n := 0
	for _, c := range decl.Components {
		if !UnmeasurableByDeclaration(c) {
			n++
		}
	}
	return n
}

// UnmeasurableByDeclaration reports whether no run could ever measure this
// component, from what it declares and nothing else.
//
// It answers the question `lydite test record` asks of a fold, and must answer
// it identically: Recordable's count is the denominator of the row announcing
// how much of the declaration a candidate covers, and record refuses a fold by
// the same predicate. Two answers that disagree announce a recording record
// then refuses, or refuse one the row never warned about.
func UnmeasurableByDeclaration(c component.Component) bool {
	if len(c.Command) > 0 {
		return true
	}
	r, ok := runner.Lookup(c.Runner)
	if !ok {
		return true
	}
	inv, ok := r.Build(runner.Instrumented, c.Args)
	return !ok || inv.CoverageReport == ""
}

// SameEntries reports whether two of one metric's baselines hold the same
// entries, so a run that would rewrite a tree's state byte for byte does not
// push to do it.
func SameEntries[M ~map[string]E, E comparable](a, b M) bool {
	if len(a) != len(b) {
		return false
	}
	for name, entry := range a {
		if other, ok := b[name]; !ok || other != entry {
			return false
		}
	}
	return true
}

// RecordingBlockedBy names the component that stops this run establishing the
// tree's baseline, if any: one with no entry in what is about to be recorded.
//
// The question is asked of the record and not of the flags, which is the
// difference between a component that carried forward and one that was merely
// entitled to. A deselected component whose baseline entry does not exist
// carries nothing, so recording anyway would write the same gap forward on
// every merge — and it can then never heal, because each run reproduces it
// from the last.
//
// A component nothing could ever measure is one exemption: it contributes to
// neither side of any comparison, so its absence is permanent and expected
// rather than a gap a run created. A component this run never selected is the
// other, and for a different reason: completeness is the fold's question,
// asked once against the declaration of the tree being recorded. A shard is
// missing most components by construction, so a run that refused over them
// would establish nothing at all — and `lydite test record` refuses the
// partial document that leaves behind, by name.
func RecordingBlockedBy(ms []Measurement, record gitstate.Baseline) (Measurement, bool) {
	for _, m := range ms {
		if m.Unmeasurable || m.Unselected {
			continue
		}
		if _, ok := record[m.Name]; !ok {
			return m, true
		}
	}
	return Measurement{}, false
}

// WithToleratedDipsRestored returns record with every component whose coverage
// dipped below its baseline by no more than the tolerance restored to the
// baseline's counts.
//
// Recording the dipped number verbatim turns the tolerance into an unbounded
// downward ratchet: each change may dip up to the tolerance and pass, the
// recorded baseline follows it down, and the next change gets another free dip
// from the lower floor — coverage bleeds one tolerance per merge with every
// gate green. Anchoring to the high-water mark caps the total tolerated drift
// at one tolerance. A dip beyond the tolerance is recorded as measured: it
// failed visibly on the change that introduced it, so accepting it is a
// deliberate reset rather than leakage.
func WithToleratedDipsRestored(record, baseline gitstate.Baseline, tolerance float64) gitstate.Baseline {
	out := make(gitstate.Baseline, len(record))
	for name, lines := range record {
		out[name] = lines
		b, ok := baseline[name]
		// The same instrument on both sides, or no anchoring. Across a change
		// of instrument the two percentages measure different quantities, so
		// anchoring to the old one records a number the new instrument never
		// produced — for a component the gate itself has just refused to
		// compare. The measured counts are recorded verbatim instead.
		if !ok || !b.Measured() || !lines.Measured() || b.Producer != lines.Producer {
			continue
		}
		if lines.Percent() < b.Percent() && !regressedBeyond(lines.Percent(), b.Percent(), tolerance) {
			out[name] = gitstate.Entry{LineCount: atPercentOf(b.Percent(), lines.Total), Producer: lines.Producer}
		}
	}
	return out
}

// atPercentOf is the ratio to anchor to, expressed over the size the component
// actually is now.
//
// Writing the baseline's own counts back would freeze the component's *weight*
// as well as its ratio: a component that grew from 1,000 lines to 2,000 while
// dipping inside the tolerance would be recorded as 1,000 lines. The language
// and global baselines are sums of these counts, so that stale weight then
// decides how much this component counts towards a figure describing a tree it
// no longer matches — and the distortion compounds over successive
// within-tolerance merges. The anchor is the percentage; the size is this
// tree's.
func atPercentOf(pct float64, total int) coverage.LineCount {
	return coverage.LineCount{Covered: int(math.Round(pct / 100 * float64(total))), Total: total}
}

// TestCounts is what each component's suite reported, for the components a run
// actually ran one for.
//
// Every component in ms, whether or not it produced a measurement: a suite
// that failed has counts and no measurement, which is exactly the pair this
// map exists to carry past a document keyed on what would be recorded.
func TestCounts(w io.Writer, ms []Measurement) map[string]junit.Counts {
	var out map[string]junit.Counts
	for _, m := range ms {
		if m.Tests == nil {
			// A report that was asked for and did not arrive is named, never
			// skipped in silence: a component contributing no counts is
			// indistinguishable in a history from one that ran no tests, and
			// the commonest cause is a repository whose own runner
			// configuration sent the report somewhere lydite does not look.
			// Stderr, because stdout carries the report and, under --json, a
			// document a warning would make unparseable.
			if m.TestsWhy != "" {
				_, _ = fmt.Fprintf(w, "warning: %s contributed no test counts: %s\n", m.Name, m.TestsWhy)
			}
			continue
		}
		if out == nil {
			out = map[string]junit.Counts{}
		}
		out[m.Name] = *m.Tests
	}
	return out
}
