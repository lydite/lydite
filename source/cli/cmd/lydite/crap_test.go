package main

import (
	"context"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/crap"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// scored is a component that produced a CRAP report with `above` functions over
// the threshold, for a test whose subject is the gate rather than the walk.
func scored(name string, above int, worst float64) measurement {
	m := measured(name, runner.Go, 9, 10)
	m.CRAP = crap.Report{Scored: 20, Worst: worst, Over: make([]crap.Function, above)}
	return m
}

// The gate is the delta and nothing else. Absolute would fail every repository
// on the day it upgrades, over debt it has always had — the rule
// coverage.floor already follows by defaulting to 0 — and a change that leaves
// the count where it was has added none.
func TestTheGateIsTheDeltaAndNotTheCount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		above int
		base  int
		want  ui.Status
	}{
		{"a change that adds one fails", 4, 3, ui.StatusFail},
		{"standing debt passes", 3, 3, ui.StatusPass},
		{"a change that clears one passes", 2, 3, ui.StatusPass},
		{"debt no baseline has ever seen still passes", 40, 40, ui.StatusPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			row := crapRow(scored("api", tc.above, 156.3),
				gitstate.CRAPBaseline{"api": {Above: tc.base}}, true)
			if row.Status != tc.want {
				t.Errorf("crap(api) = %+v, want %s", row, tc.want)
			}
		})
	}
}

// A failing row names the work it is cleared by. The baseline stores two
// scalars on purpose — a list of names cannot be compared across trees, since a
// function renamed or moved would read as one fixed and one introduced — so
// the row says what is over the threshold now, and any one of them coming under
// it clears the gate.
func TestAFailingRowNamesTheFunctionsToActOn(t *testing.T) {
	t.Parallel()
	m := measured("api", runner.Go, 9, 10)
	m.CRAP = crap.Report{Scored: 9, Worst: 156.3, Over: []crap.Function{
		{Name: "Tangled", File: "api/lib.go", Line: 42, Complexity: 12, Value: 156.3,
			Lines: coverage.LineCount{Covered: 0, Total: 20}},
	}}
	row := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0}}, true)
	if row.Status != ui.StatusFail {
		t.Fatalf("crap(api) = %+v, want a failure", row)
	}
	detail := strings.Join(row.Detail, "\n")
	for _, want := range []string{"api/lib.go:42", "Tangled", "complexity 12"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to name %q", detail, want)
		}
	}
	// The value carries both ledger scalars and the number it is held to.
	for _, want := range []string{"above 30", "worst 156.3", "baseline 0"} {
		if !strings.Contains(row.Value, want) {
			t.Errorf("crap(api) = %q, want it to say %q", row.Value, want)
		}
	}
}

// A component with no entry is new and gates nothing, exactly as a coverage
// measurement with no baseline is: every repository is in that state the first
// time it runs a lydite that computes CRAP, and failing there would be the
// absolute gate arriving through the mechanism meant to prevent it.
//
// A baseline a different instrument produced is new too, and never regressed:
// the coverage half of every score was taken by that instrument, so the
// difference is a change of definition rather than debt anybody added.
func TestAScoreWithNothingComparableIsNewAndNotAFailure(t *testing.T) {
	t.Parallel()
	m := scored("api", 9, 200)
	m.Producer = "go1.26.6"

	fresh := crapRow(m, nil, true)
	if fresh.Status != ui.StatusNew || !strings.Contains(fresh.Value, "no baseline yet") {
		t.Errorf("crap(api) with no baseline = %+v, want new", fresh)
	}
	moved := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0, Producer: "go1.25.1"}}, true)
	if moved.Status != ui.StatusNew || !strings.Contains(moved.Value, "not compared") {
		t.Errorf("crap(api) across a changed instrument = %+v, want new", moved)
	}
	// And an ungated run compares nothing at all, so it renders as context
	// rather than as the green a gated run reports.
	ungated := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0, Producer: "go1.26.6"}}, false)
	if ungated.Status != ui.StatusContext {
		t.Errorf("crap(api) in an ungated run = %+v, want context", ungated)
	}
}

// A component lydite scores none of is context and never amber. Nothing about
// the repository could make the row green, so spending the tag that exists to
// be noticed on it is what teaches a reader to skim past it — and it is still a
// row, because a component silently absent reads as one that scored clean.
//
// A Go component whose score could not be taken is the opposite: that is a gate
// that did not run, and it is amber.
func TestALanguageWithNoComplexitySourceIsContextAndAFailedScoreIsAmber(t *testing.T) {
	t.Parallel()
	// A TypeScript component whose suite failed as well: the row must say the
	// metric has no source for it, not that the suite failed — "not scored —
	// the suite failed" reads as though fixing the suite would produce a
	// score.
	web := unmeasuredComponent(component.Component{Name: "web", Dir: "web", Runner: runner.Vitest}, "the suite failed")
	row := crapRow(web, nil, true)
	if row.Status != ui.StatusContext {
		t.Errorf("crap(web) = %+v, want context — the metric has no source for it", row)
	}
	if !strings.Contains(row.Value, "typescript") || strings.Contains(row.Value, "the suite failed") {
		t.Errorf("crap(web) = %q, want it to name the language rather than the suite", row.Value)
	}

	broken := measured("api", runner.Go, 9, 10)
	broken.CRAPWhy = "parsing api/lib.go to score its functions: expected ';'"
	if got := crapRow(broken, nil, true); got.Status != ui.StatusUnmeasured || !strings.Contains(got.Value, "api/lib.go") {
		t.Errorf("crap(api) = %+v, want an amber row naming what could not be read", got)
	}

	// And a component affected selection did not run names what it carries,
	// for the reason a carried coverage figure does: that entry is what the
	// next change is gated against.
	unrun := unmeasuredComponent(component.Component{Name: "sdk", Dir: "sdk", Runner: runner.GoTest},
		"the component was not selected for this run")
	unrun.Carryable = true
	if got := crapRow(unrun, gitstate.CRAPBaseline{"sdk": {Above: 7}}, true); !strings.Contains(got.Value, "carrying the baseline's 7") {
		t.Errorf("crap(sdk) = %q, want it to name what it carries forward", got.Value)
	}
}

// The figure over the repository counts the components CRAP could apply to, so
// a repository whose TypeScript components cannot be scored is not reported as
// two thirds ungated. It is the same rule a composed coverage figure follows.
func TestTheSummaryCountsWhatCouldBeScored(t *testing.T) {
	t.Parallel()
	web := measured("web", runner.TypeScript, 1, 2)
	web.CRAPWhy = "lydite scores Go alone, and web is typescript"
	row, ok := crapSummaryOf([]measurement{scored("api", 3, 41.0), scored("sdk", 2, 156.3), web})
	if !ok {
		t.Fatal("no summary row over a repository with two scored components")
	}
	if !strings.Contains(row.Value, "5 function(s) above 30") {
		t.Errorf("crap = %q, want the counts summed", row.Value)
	}
	if !strings.Contains(row.Value, "2 of 2 component(s)") {
		t.Errorf("crap = %q, want a denominator of the components it could score", row.Value)
	}
	if !strings.Contains(row.Value, "worst 156.3") {
		t.Errorf("crap = %q, want the worst across the repository", row.Value)
	}
	if row.Status != ui.StatusContext {
		t.Errorf("crap = %+v, want context — every component's own row carries the gate", row)
	}

	// A repository lydite can score nothing of gets no row at all: that is a
	// property of the metric, not a gap this run left.
	if _, ok := crapSummaryOf([]measurement{web}); ok {
		t.Error("a repository with no component CRAP applies to got a summary row")
	}
	// One it could score and did not is amber, for the reason a floor that
	// cleared nothing is: a gate that examined nothing must not read as one
	// that did.
	failed := unmeasuredComponent(component.Component{Name: "api", Dir: "api", Runner: runner.GoTest}, "the suite failed")
	if row, ok := crapSummaryOf([]measurement{failed}); !ok || row.Status != ui.StatusUnmeasured {
		t.Errorf("crap = %+v (ok=%v), want an amber row", row, ok)
	}
}

// A score rides on a coverage entry and never travels alone. It is computed
// from the coverage report, so an entry with no counts has nothing to hang one
// on — and `missingFromRecord` asks only whether a component has an entry, so a
// score-only entry would satisfy the completeness check for a component whose
// coverage nobody measured.
//
// The carry rule is coverage's: a component this run did not select is
// unchanged from the tree the baseline describes, while one that ran and failed
// may be exactly what changed.
func TestOnlyAnUnselectedComponentCarriesItsScoreForward(t *testing.T) {
	t.Parallel()
	anchor := gitstate.CRAPBaseline{
		"unselected": {Above: 4, Worst: 90},
		"failed":     {Above: 1, Worst: 40},
		"uncovered":  {Above: 2, Worst: 50},
	}
	record := gitstate.Baseline{"api": entry(9, 10), "unselected": entry(5, 10)}
	carried := map[string]bool{"unselected": true}

	unselected := unmeasuredComponent(component.Component{Name: "unselected", Dir: "u", Runner: runner.GoTest},
		"the component was not selected for this run")
	unselected.Carryable = true
	failed := unmeasuredComponent(component.Component{Name: "failed", Dir: "f", Runner: runner.GoTest}, "the suite failed")
	// Entitled to carry, but with no coverage entry to hang a score on.
	uncovered := unmeasuredComponent(component.Component{Name: "uncovered", Dir: "n", Runner: runner.GoTest},
		"the component was not selected for this run")
	uncovered.Carryable = true

	got := crapRecord([]measurement{scored("api", 1, 33), unselected, failed, uncovered}, record, carried, anchor)
	if e, ok := got["unselected"]; !ok || e.Above != 4 {
		t.Errorf("unselected = %+v (ok=%v), want the baseline's entry carried", e, ok)
	}
	if _, ok := got["failed"]; ok {
		t.Error("a component that ran and failed carried its old score forward")
	}
	if _, ok := got["uncovered"]; ok {
		t.Error("a score was recorded for a component with no coverage entry beside it")
	}
	if e, ok := got["api"]; !ok || e.Above != 1 {
		t.Errorf("api = %+v (ok=%v), want what this run scored", e, ok)
	}
}

// The whole gate, through the command, against a real repository: the base
// tree's score is measured and recorded, and a change that adds one untested
// tangled function fails on the delta while the rest of the run is otherwise
// unremarkable.
//
// End to end because that is the only way the score reaches the row at all —
// the walk, the profile, the baseline document and the comparison are four
// separate pieces, and each one of them can be correct while the run reports
// nothing.
func TestTheComplexityGateAgainstARealRepository(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), root, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("the run on the default branch: %v\n%s", err, errOut)
	}
	if out, errOut, err := runRecordCmd(t, root); err != nil {
		t.Fatalf("recording the default branch: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	// The recorded state holds the score beside the coverage counts, in its
	// own document: a repository upgrading to a lydite that computes CRAP has
	// one and not the other, and reading them together would charge it a base
	// tree measurement for a metric that gates nothing yet.
	tree, err := gitstate.TreeSHA(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	scores, hit, err := gitstate.ReadCRAP(context.Background(), root, tree)
	if err != nil || !hit {
		t.Fatalf("the CRAP baseline = (hit=%v, %v), want the default branch's run recorded", hit, err)
	}
	if e, ok := scores["svc"]; !ok || e.Worst == 0 {
		t.Fatalf("svc = %+v (ok=%v), want a recorded score", e, ok)
	}

	// A change adding one untested function whose branches put it over the
	// threshold. Nothing else about the component moves that the gate would
	// notice first: the function is new, so the aggregate cannot fall.
	run("switch", "--quiet", "-c", "change")
	write(t, root, "svc/tangled.go", `package svc

// Tangled is untested and full of branches, which is the pair CRAP scores.
func Tangled(a, b, c, d, e, f int) int {
	n := 0
	if a > 0 {
		n++
	}
	if b > 0 {
		n++
	}
	if c > 0 {
		n++
	}
	if d > 0 {
		n++
	}
	if e > 0 {
		n++
	}
	if f > 0 {
		n++
	}
	return n
}
`)
	run("add", "-A")
	run("commit", "-m", "one tangled function")

	out, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json")
	if err == nil {
		t.Fatalf("the gate passed a change adding an untested tangled function\nstdout: %s\nstderr: %s", out, errOut)
	}
	rows := jsonRows(t, out)
	row, ok := rows["crap(svc)"]
	if !ok {
		t.Fatalf("no crap(svc) row: %s", out)
	}
	if row.Status != "fail" {
		t.Fatalf("crap(svc) = %+v, want a failure over the delta", row)
	}
	if !strings.Contains(strings.Join(row.Detail, "\n"), "Tangled") {
		t.Errorf("crap(svc) detail = %v, want the function named", row.Detail)
	}
	// And the figure over the repository is beside it, gating nothing.
	if got, ok := rows["crap"]; !ok || got.Status != "context" {
		t.Errorf("crap = %+v (ok=%v), want a context row over the repository", got, ok)
	}
}
