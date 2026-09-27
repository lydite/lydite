package measure

import (
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// lines is a measured count, for a test that cares about the ratio rather than
// about which report produced it.
func lines(covered, total int) coverage.LineCount {
	return coverage.LineCount{Covered: covered, Total: total}
}

// entry is a baseline entry with no producer, for a test whose subject is the
// arithmetic rather than the instrument. A test about the producer names one.
func entry(covered, total int) gitstate.Entry {
	return gitstate.Entry{LineCount: lines(covered, total)}
}

// producing builds a measured entry attributed to one instrument.
func producing(covered, total int, producer string) gitstate.Entry {
	return gitstate.Entry{LineCount: lines(covered, total), Producer: producer}
}

// measured is a component that produced a measurement.
func measured(name string, lang runner.Lang, covered, total int) Measurement {
	return Measurement{Name: name, Dir: name, Lang: lang, Lines: lines(covered, total)}
}

// The three altitudes are sums over one stored quantity, so they cannot
// disagree — and the language figure weights by lines rather than averaging
// percentages. A 1000-line component at 90% and a 10-line one at 0% is 89.1%,
// not the 45% a mean would report.
func TestComposedFiguresAreLineWeighted(t *testing.T) {
	ms := []Measurement{
		measured("big", runner.Go, 900, 1000),
		measured("tiny", runner.Go, 0, 10),
	}
	got, fresh, carried := composed(ms, nil, Everything)
	if got != lines(900, 1010) {
		t.Fatalf("composed = %+v, want {900 1010}", got)
	}
	if fresh != 2 || carried != 0 {
		t.Errorf("contributors = %d fresh, %d carried; want 2 and 0", fresh, carried)
	}
	if pct := got.Percent(); pct < 89.1 || pct > 89.2 {
		t.Errorf("percent = %v, want ~89.1 — a mean of the two would be 45", pct)
	}
}

// A component that produced no measurement contributes the counts already
// recorded for the tree it is unchanged from, and the row says how many of
// each the figure is made of. A composed figure that does not say what it
// measured is indistinguishable from one that measured everything.
func TestACarriedComponentIsCountedAndNamed(t *testing.T) {
	current := []Measurement{
		measured("api", runner.Go, 50, 100),
		{Name: "sdk", Dir: "sdk", Lang: runner.Go, Lines: lines(80, 100)},
	}
	baseline := gitstate.Baseline{"api": entry(50, 100), "sdk": entry(80, 100)}
	row := ComposedRow("coverage(subset)", current, map[string]bool{"sdk": true}, baseline, onlyLang(runner.Go), 0.1)
	if row.Status != ui.StatusPass {
		t.Fatalf("row = %+v, want a pass", row)
	}
	if !strings.Contains(row.Value, "2 of 2 component(s), 1 carried forward") {
		t.Errorf("value = %q, want it to name what it measured and what it carried", row.Value)
	}
}

// The baseline side of a composed comparison sums exactly the components the
// current side covers. Summing the whole baseline instead would compare this
// run's components against the base tree's, so every narrowed run would read
// as a regression the size of the component it did not run.
func TestAComposedComparisonOnlyCoversWhatItMeasured(t *testing.T) {
	// api is measured and unchanged; sdk did not run and the baseline has no
	// entry for it, so it contributes to neither side.
	current := []Measurement{
		measured("api", runner.Go, 50, 100),
		UnmeasuredComponent(component.Component{Name: "sdk", Dir: "sdk", Runner: runner.GoTest}, "not affected"),
	}
	baseline := gitstate.Baseline{"api": entry(50, 100), "sdk": entry(5, 1000)}
	row := ComposedRow("coverage(subset)", current, nil, baseline, onlyLang(runner.Go), 0.1)
	if row.Status == ui.StatusFail {
		t.Fatalf("row = %+v — the unrun component's baseline must not drag the comparison", row)
	}
	if !strings.Contains(row.Value, "1 of 2 component(s)") {
		t.Errorf("value = %q, want it to say only one component was in the figure", row.Value)
	}
}

// A composed figure whose baseline does not cover every component in it is
// reported as new rather than compared. A partial comparison is a different
// quantity, and rendering one as a comparison is exactly the class of error
// this gate exists to avoid.
func TestAComposedFigureWithAnIncompleteBaselineIsNotCompared(t *testing.T) {
	current := []Measurement{
		measured("api", runner.Go, 50, 100),
		measured("sdk", runner.Go, 90, 100),
	}
	row := ComposedRow("coverage(subset)", current, nil, gitstate.Baseline{"api": entry(50, 100)}, onlyLang(runner.Go), 0.1)
	if row.Status != ui.StatusNew {
		t.Errorf("row = %+v, want new — the baseline covers one of the two components", row)
	}
}

// Only a component this run did not select carries its baseline forward. One
// that ran and failed may be exactly what changed, so its old entry is a guess
// — and carrying it renders as a pass, so a language whose only component
// failed to build would report that component's last good figure with a ✓
// beside it.
func TestOnlyAnUnselectedComponentCarriesForward(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "web", Dir: "web", Runner: runner.Vitest},
	}}
	for _, tc := range []struct {
		name      string
		carryable bool
		want      ui.Status
	}{
		{"a component selection skipped", true, ui.StatusPass},
		{"a component that failed", false, ui.StatusUnmeasured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := UnmeasuredComponent(decl.Components[0], "why")
			m.Carryable = tc.carryable
			baseline := gitstate.Baseline{"web": entry(80, 100)}
			current := []Measurement{m}
			carried := map[string]bool{}
			if tc.carryable {
				current = []Measurement{FromEntry(Measurement{Name: "web", Dir: "web", Lang: runner.TypeScript}, baseline["web"])}
				carried["web"] = true
			}
			row := ComposedRow("coverage(subset)", current, carried, baseline, onlyLang(runner.TypeScript), 0.1)
			if row.Status != tc.want {
				t.Errorf("row = %+v, want %q", row, tc.want)
			}
		})
	}
}

// The aggregate says the repository did not get worse; the per-component patch
// rows say each component's new code met its own standard. Neither answers what
// a reviewer asks about a change spanning several components — was the new code
// in this change tested — so a composed patch figure gates it.
func TestComposedPatchGatesNewCodeAcrossComponents(t *testing.T) {
	// Each component's own patch clears its own baseline on tolerance, and the
	// change as a whole does not: this is the case the per-component rows and
	// the aggregate both let through.
	base := coverage.LineCount{Covered: 825, Total: 1000} // 82.5%
	parts := []PatchPart{
		{Name: "a", Lang: runner.Go, Hit: 40, Total: 50, Base: base},
		{Name: "b", Lang: runner.Go, Hit: 40, Total: 50, Base: base},
	}
	got := ComposedPatchRow("go patch", parts, 0.1)
	if got.Status != ui.StatusFail {
		t.Fatalf("80.0%% of new lines against an 82.5%% baseline must fail: %+v", got)
	}
	for _, want := range []string{"80.0%", "baseline 82.5%", "below it by 2.5%"} {
		if !strings.Contains(got.Value, want) {
			t.Errorf("the row does not say %q: %q", want, got.Value)
		}
	}
}

// Summed over changed lines, never averaged over components: a mean lets a
// two-line component outvote a two-hundred-line one, which is the error
// ADR 0007 records for the aggregate.
func TestComposedPatchIsWeightedByChangedLines(t *testing.T) {
	base := coverage.LineCount{Covered: 50, Total: 100} // 50%
	parts := []PatchPart{
		{Name: "big", Lang: runner.Go, Hit: 190, Total: 200, Base: base},
		{Name: "tiny", Lang: runner.Go, Hit: 0, Total: 2, Base: base},
	}
	got := ComposedPatchRow("go patch", parts, 0.1)
	if got.Status != ui.StatusPass {
		t.Fatalf("190/202 new lines against a 50%% baseline must pass; a mean of 95%% and 0%% would fail it. got %+v", got)
	}
	if !strings.Contains(got.Value, "190/202 new lines") {
		t.Errorf("the row does not show the summed counts: %q", got.Value)
	}
}

// A component with no baseline contributes new lines to the figure and nothing
// to the comparison, so comparing anyway would report movement nobody caused —
// the rule ComposedRow already follows for the aggregate.
func TestComposedPatchWillNotCompareAgainstAPartialBaseline(t *testing.T) {
	parts := []PatchPart{
		{Name: "a", Lang: runner.Go, Hit: 40, Total: 50, Base: coverage.LineCount{Covered: 80, Total: 100}},
		{Name: "fresh", Lang: runner.Go, Hit: 50, Total: 50},
	}
	got := ComposedPatchRow("go patch", parts, 0.1)
	if got.Status != ui.StatusNew {
		t.Fatalf("a partial baseline must not be compared against: %+v", got)
	}
	if !strings.Contains(got.Value, "fresh") {
		t.Errorf("the row does not name the component missing a baseline: %q", got.Value)
	}
}

// onlyLang filters a composed figure to a subset of the components.
//
// A test helper rather than production code: coverage composes at the
// component and the repository, and a language is neither, so nothing in a run
// builds a figure this way. The composition itself is still what these tests
// are about.
func onlyLang(l runner.Lang) func(Measurement) bool {
	return func(m Measurement) bool { return m.Lang == l }
}

// A composed figure refuses to compare when any component in it was measured by
// a different instrument, and says which — the same rule it already applies to a
// component with no baseline, with words that tell the two apart. A reader
// cannot act on "no baseline yet" for a component that has had one for months.
func TestAComposedFigureNamesAReinstrumentedComponent(t *testing.T) {
	api := measured("api", "go", 50, 100)
	api.Producer = "go 1.26.6"
	web := measured("web", "typescript", 80, 100)
	web.Producer = "vitest 4.1.11, @vitest/coverage-v8 4.1.11"
	baseline := gitstate.Baseline{
		"api": producing(50, 100, "go 1.26.6"),
		"web": producing(80, 100, "vitest 3.2.7, @vitest/coverage-v8 3.2.7"),
	}

	row := ComposedRow(RepoLabel("coverage"), []Measurement{api, web}, nil, baseline, Everything, 0.1)
	if row.Status != "new" {
		t.Errorf("coverage(repo) = %+v, want new — its baseline does not cover every component", row)
	}
	if !strings.Contains(row.Value, "different instrument") || !strings.Contains(row.Value, "web") {
		t.Errorf("row = %q, want the reinstrumented component named", row.Value)
	}
	if strings.Contains(row.Value, "no baseline yet") {
		t.Errorf("row = %q, want a baseline that exists not described as absent", row.Value)
	}
}

// A component declaring a raw command has no producer, because lydite does not
// know what such a run would invoke, let alone what wrote a report.
func TestAComponentWithARawCommandHasNoProducer(t *testing.T) {
	c := component.Component{Name: "docs", Dir: "docs", Command: []string{"make", "docs"}}
	if got := producerOf(t.TempDir(), c, config.Default(), nil); got != "" {
		t.Errorf("producer = %q, want nothing for a component lydite does not invoke", got)
	}
}
