package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
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
			row, _ := crapRow(scored("api", tc.above, 156.3),
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
	row, _ := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0}}, true)
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

	// And how many the change added, against a baseline that is not zero:
	// the number a reader acts on is the delta, and against an empty baseline
	// the delta and the count are the same number.
	over := scored("api", 9, 400)
	if got, _ := crapRow(over, gitstate.CRAPBaseline{"api": {Above: 4}}, true); !strings.Contains(got.Value, "5 more") {
		t.Errorf("crap(api) = %q, want it to say the change added 5", got.Value)
	}
}

// The excluded count rides on every row that has one, because a repository can
// annotate its way to nothing above the threshold and that number is what makes
// it visible when one does — and says nothing when there is nothing to say,
// since a trailing "0 excluded" on every clean row is a clause readers learn to
// skip.
func TestARowSaysHowManyFunctionsWereExcluded(t *testing.T) {
	t.Parallel()
	clean := scored("api", 1, 41.5)
	if got := crapValue(clean.CRAP); strings.Contains(got, "excluded") {
		t.Errorf("crap(api) = %q, want no clause when nothing was excluded", got)
	}
	declared := scored("api", 1, 41.5)
	declared.CRAP.Excluded = 2
	if got := crapValue(declared.CRAP); !strings.Contains(got, "2 excluded") {
		t.Errorf("crap(api) = %q, want the two declarations counted", got)
	}
}

// A failing row names enough functions to act on and no more, and says how many
// it left out. The whole of a component's debt is a page, and a detail nobody
// reads to the end is a detail nobody reads.
func TestAFailingRowNamesTheWorstFewAndCountsTheRest(t *testing.T) {
	t.Parallel()
	rowFor := func(n int) ui.Row {
		t.Helper()
		m := measured("api", runner.Go, 9, 10)
		m.CRAP = crap.Report{Scored: 40, Worst: 200}
		for i := range n {
			m.CRAP.Over = append(m.CRAP.Over, crap.Function{
				Name: fmt.Sprintf("F%d", i), File: "api/lib.go", Line: i + 1,
				Complexity: 12, Value: float64(200 - i),
				Lines: coverage.LineCount{Covered: 0, Total: 20},
			})
		}
		row, _ := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0}}, true)
		return row
	}
	// Exactly the cap: every one is named, and there is no tail saying none
	// were left out.
	capped := strings.Join(rowFor(worstOffenders).Detail, "\n")
	if strings.Contains(capped, "more above") {
		t.Errorf("detail = %q, want no tail when nothing was left out", capped)
	}
	if !strings.Contains(capped, "F4") {
		t.Errorf("detail = %q, want every one of the %d named", capped, worstOffenders)
	}
	// Two beyond it: the tail counts exactly those two.
	over := strings.Join(rowFor(worstOffenders+2).Detail, "\n")
	if !strings.Contains(over, "and 2 more above 30") {
		t.Errorf("detail = %q, want the two it left out counted", over)
	}
	if strings.Contains(over, "F5") || strings.Contains(over, "F6") {
		t.Errorf("detail = %q, want the ones beyond the cap left out rather than named", over)
	}
}

// An ungated run reports each component's score beside its coverage. Without
// the row a workflow that reads no baseline says nothing at all about
// complexity, which is indistinguishable from a repository that has none — and
// the row is where the two ledger scalars reach a reader at all on the path
// that gates nothing.
func TestAnUngatedRunStillReportsWhatItScored(t *testing.T) {
	t.Parallel()
	rep := ui.NewReport("test")
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
	}}
	addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl, decl.Components,
		[]measurement{scored("api", 3, 41.5)}, config.Default(), coverageOptions{Instrument: true})
	rows := rowsOf(rep)
	got, ok := rows["crap(api)"]
	if !ok {
		t.Fatalf("no crap(api) row in an ungated run: %v", rep.Rows())
	}
	if got.Status != ui.StatusContext || !strings.Contains(got.Value, "3 function(s) above 30") {
		t.Errorf("crap(api) = %+v, want the score, uncompared", got)
	}
	if _, ok := rows["crap"]; !ok {
		t.Errorf("no figure over the repository in an ungated run: %v", rep.Rows())
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

	fresh, _ := crapRow(m, nil, true)
	if fresh.Status != ui.StatusNew || !strings.Contains(fresh.Value, "no baseline yet") {
		t.Errorf("crap(api) with no baseline = %+v, want new", fresh)
	}
	moved, _ := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0, Producer: "go1.25.1"}}, true)
	if moved.Status != ui.StatusNew || !strings.Contains(moved.Value, "not compared") {
		t.Errorf("crap(api) across a changed instrument = %+v, want new", moved)
	}
	// And an ungated run compares nothing at all, so it renders as context
	// rather than as the green a gated run reports.
	ungated, _ := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0, Producer: "go1.26.6"}}, false)
	if ungated.Status != ui.StatusContext {
		t.Errorf("crap(api) in an ungated run = %+v, want context", ungated)
	}
}

// A component lydite scores none of is context and never amber. Nothing about
// the repository could make the row green, so spending the tag that exists to
// be noticed on it is what teaches a reader to skim past it — and it is still a
// row, because a component silently absent reads as one that scored clean.
//
// A component whose score could not be taken is the opposite: that is a gate
// that did not run, and it is amber.
func TestAComponentWithNoLanguageIsContextAndAFailedScoreIsAmber(t *testing.T) {
	t.Parallel()
	// A raw-command component whose suite failed as well: the row must say the
	// metric has no source for it, not that the suite failed — "not scored —
	// the suite failed" reads as though fixing the suite would produce a
	// score.
	docs := unmeasuredComponent(component.Component{Name: "docs", Dir: "docs", Command: []string{"make"}}, "the suite failed")
	row, _ := crapRow(docs, nil, true)
	if row.Status != ui.StatusContext {
		t.Errorf("crap(docs) = %+v, want context — the metric has no source for it", row)
	}
	if !strings.Contains(row.Value, "unstated") || strings.Contains(row.Value, "the suite failed") {
		t.Errorf("crap(docs) = %q, want it to say the language is unstated rather than name the suite", row.Value)
	}

	broken := measured("api", runner.Go, 9, 10)
	broken.CRAPWhy = "parsing api/lib.go to score its functions: expected ';'"
	if got, _ := crapRow(broken, nil, true); got.Status != ui.StatusUnmeasured || !strings.Contains(got.Value, "api/lib.go") {
		t.Errorf("crap(api) = %+v, want an amber row naming what could not be read", got)
	}

	// And a component affected selection did not run names what it carries,
	// for the reason a carried coverage figure does: that entry is what the
	// next change is gated against.
	unrun := unmeasuredComponent(component.Component{Name: "sdk", Dir: "sdk", Runner: runner.GoTest},
		"the component was not selected for this run")
	unrun.Carryable = true
	if got, _ := crapRow(unrun, gitstate.CRAPBaseline{"sdk": {Above: 7}}, true); !strings.Contains(got.Value, "carrying the baseline's 7") {
		t.Errorf("crap(sdk) = %q, want it to name what it carries forward", got.Value)
	}
}

// The figure over the repository counts the components CRAP could apply to, so
// a repository whose raw-command components cannot be scored is not reported as
// two thirds ungated. It is the same rule a composed coverage figure follows.
func TestTheSummaryCountsWhatCouldBeScored(t *testing.T) {
	t.Parallel()
	docs := unmeasurableComponent(component.Component{Name: "docs", Dir: "docs", Command: []string{"make"}},
		"the component declares a raw command, which has no instrumented variant")
	row, ok := crapSummaryOf([]measurement{scored("api", 3, 41.0), scored("sdk", 2, 156.3), docs}, nil, nil)
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
	// A run that carried nothing says nothing about carrying: "0 carried
	// forward" on every complete run is a clause readers learn to skip.
	if strings.Contains(row.Value, "carried forward") {
		t.Errorf("crap = %q, want no carry clause on a run that carried nothing", row.Value)
	}

	// A repository lydite can score nothing of gets no row at all: that is a
	// property of the metric, not a gap this run left.
	if _, ok := crapSummaryOf([]measurement{docs}, nil, nil); ok {
		t.Error("a repository with no component CRAP applies to got a summary row")
	}
	// One it could score and did not is amber, for the reason a floor that
	// cleared nothing is: a gate that examined nothing must not read as one
	// that did.
	failed := unmeasuredComponent(component.Component{Name: "api", Dir: "api", Runner: runner.GoTest}, "the suite failed")
	if row, ok := crapSummaryOf([]measurement{failed}, nil, nil); !ok || row.Status != ui.StatusUnmeasured {
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

	// A component that ran, measured its coverage, and could not be scored.
	// It has an entry in `record` and is not carried, so nothing licenses
	// standing in the baseline's number for it: its content may be exactly
	// what changed.
	unscorable := measured("unscorable", runner.Go, 9, 10)
	unscorable.CRAPWhy = "parsing unscorable/lib.go to score its functions: expected ';'"
	record["unscorable"] = entry(9, 10)
	anchor["unscorable"] = gitstate.CRAPEntry{Above: 3, Worst: 70}

	got := crapRecord([]measurement{scored("api", 1, 33), unselected, failed, uncovered, unscorable}, record, carried, anchor)
	if _, ok := got["unscorable"]; ok {
		t.Error("a component that ran and could not be scored recorded the baseline's score")
	}
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

// A Go component whose source the walk cannot read is scored by nobody, and
// the reason travels to the row rather than being swallowed. Driven through
// `score` rather than by assigning the field, so the wiring between
// crap.Measure's error and CRAPWhy is what is asserted.
func TestAScoreThatCouldNotBeTakenCarriesItsReason(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "svc/lib.go", "package svc\n\nfunc broken(\n")
	m := measured("svc", runner.Go, 9, 10)
	m.Hits = coverage.LineHits{"svc/lib.go": {1: 1, 2: 1, 3: 1}}

	rep, why := score(root, m)
	if rep.Measured() {
		t.Errorf("report = %+v, want nothing scored", rep)
	}
	if !strings.Contains(why, "svc/lib.go") {
		t.Errorf("why = %q, want the file the walk could not read", why)
	}
	// And the row is amber: this is a gate that could not run, not a language
	// the metric has no source for.
	m.CRAPWhy = why
	if got, _ := crapRow(m, nil, true); got.Status != ui.StatusUnmeasured {
		t.Errorf("crap(svc) = %+v, want amber", got)
	}
}

// The figure over the repository is the same figure whether the run was
// sharded or not. A component affected selection did not run still has a score,
// so both paths count the entry carried forward for it and both say how many
// they carried — one rule, because two that agreed today would come apart the
// day one learned something, and nothing in either report would show it.
func TestTheFigureOverTheRepositoryDoesNotDependOnSharding(t *testing.T) {
	t.Parallel()
	anchor := gitstate.CRAPBaseline{"sdk": {Above: 4, Worst: 90}}
	unselected := unmeasuredComponent(component.Component{Name: "sdk", Dir: "sdk", Runner: runner.GoTest},
		"the component was not selected for this run")
	unselected.Carryable = true
	ms := []measurement{scored("api", 3, 41.5), unselected}
	carried := map[string]bool{"sdk": true}

	row, ok := crapSummaryOf(ms, carried, anchor)
	if !ok {
		t.Fatal("no summary row over a run that carried one of its two components")
	}
	// Both counted, and the carried one named — the shape composedValue gives
	// a coverage figure for the same reason.
	for _, want := range []string{"7 function(s) above 30", "2 of 2 component(s)", "1 carried forward", "worst 90.0"} {
		if !strings.Contains(row.Value, want) {
			t.Errorf("crap = %q, want it to say %q", row.Value, want)
		}
	}

	// The fold composes the same figure from the scalars the shards wrote,
	// which is where the carried entry arrives with `carried` set.
	folded, ok := crapSummaryRow(2,
		[]gitstate.CRAPEntry{{Above: 3, Worst: 41.5}},
		[]gitstate.CRAPEntry{{Above: 4, Worst: 90}})
	if !ok || folded.Value != row.Value {
		t.Errorf("the fold says %q and the run says %q — one tree, two figures", folded.Value, row.Value)
	}
}

// A component in a language lydite scores none of names the language, and one
// declaring a raw command says its language is unstated — neither is reported
// as a score that could not be taken, which is what a Go component gets.
func TestARawCommandComponentSaysItsLanguageIsUnstated(t *testing.T) {
	t.Parallel()
	raw := unmeasurableComponent(component.Component{Name: "docs", Dir: "docs", Command: []string{"make"}},
		"the component declares a raw command, which has no instrumented variant")
	row, _ := crapRow(raw, nil, true)
	if row.Status != ui.StatusContext {
		t.Errorf("crap(docs) = %+v, want context — the metric has no source for it", row)
	}
	if !strings.Contains(row.Value, "declares a raw command") {
		t.Errorf("crap(docs) = %q, want it to say the language is unstated", row.Value)
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
	snap, err := gitstate.ReadSnapshot(context.Background(), root, tree)
	if err != nil || len(snap.CRAP) == 0 {
		t.Fatalf("the CRAP baseline = (%v, %v), want the default branch's run recorded", snap.CRAP, err)
	}
	if e, ok := snap.CRAP["svc"]; !ok || e.Worst == 0 {
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

// A repository that has a coverage baseline and no CRAP one is every
// repository, the first time it runs a lydite that computes CRAP. The coverage
// baseline alone decides whether the base tree is measured again: re-measuring
// it for a metric that would gate nothing on that run charges every one of them
// a full suite run for it. The components read `new` and gate nothing for one
// change, which is the shape a changed producer already has.
func TestACRAPMissAloneDoesNotMeasureTheBaseTree(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), root, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	// A coverage baseline for the base tree, and deliberately no CRAP one —
	// written directly rather than by a run, which is the only way to reach a
	// state `lydite test record` no longer produces.
	base, err := gitstate.TreeSHA(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitstate.Write(context.Background(), root, base,
		gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": {LineCount: lines(2, 4), Producer: goProducer(t, root)}}}, nil); err != nil {
		t.Fatal(err)
	}

	run("switch", "--quiet", "-c", "change")
	write(t, root, "svc/notes.md", "not code\n")
	run("add", "-A")
	run("commit", "-m", "a change that touches no Go")

	out, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json")
	if err != nil {
		t.Fatalf("the gate failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	rows := jsonRows(t, out)
	// The coverage baseline was a hit, so nothing re-measured the base tree.
	if got, ok := rows["baseline"]; ok && strings.Contains(got.Value, "measuring it now") {
		t.Errorf("baseline = %q, want a hit — a CRAP miss must not measure the base tree", got.Value)
	}
	// And CRAP reports itself new rather than comparing against nothing.
	if got := rows["crap(svc)"]; got.Status != "new" {
		t.Errorf("crap(svc) = %+v, want new — there is no CRAP baseline for this tree", got)
	}
}

// goProducer is what the Go toolchain in this environment names itself, so a
// hand-written baseline entry is comparable with one a run would take.
func goProducer(t *testing.T, root string) string {
	t.Helper()
	r, ok := runner.Lookup(runner.GoTest)
	if !ok {
		t.Fatal("no go-test runner")
	}
	return r.Producer(filepath.Join(root, "svc"), root, "")
}

// untested is a hit map covering the whole of a file and executing none of it,
// which is the half of a score a walk cannot supply: a tangled function nobody
// ran is what puts one over the threshold.
func untested(file string, through int) coverage.LineHits {
	hits := map[int]int{}
	for line := 1; line <= through; line++ {
		hits[line] = 0
	}
	return coverage.LineHits{file: hits}
}

// tangledRust and tangledTypeScript are one untested function each, branchy
// enough to sit above the threshold: six decision points is complexity seven,
// and at no coverage that is 56.
const tangledRust = `// tangled is untested and full of branches, which is the pair CRAP scores.
pub fn tangled(a: i64, b: i64, c: i64, d: i64, e: i64, f: i64) -> i64 {
    let mut n = 0;
    if a > 0 { n += 1; }
    if b > 0 { n += 1; }
    if c > 0 { n += 1; }
    if d > 0 { n += 1; }
    if e > 0 { n += 1; }
    if f > 0 { n += 1; }
    n
}
`

const tangledTypeScript = `// tangled is untested and full of branches, which is the pair CRAP scores.
export function tangled(a: number, b: number, c: number, d: number, e: number, f: number): number {
  let n = 0;
  if (a > 0) { n += 1; }
  if (b > 0) { n += 1; }
  if (c > 0) { n += 1; }
  if (d > 0) { n += 1; }
  if (e > 0) { n += 1; }
  if (f > 0) { n += 1; }
  return n;
}
`

// Rust and TypeScript reach the rows the same way Go does: the walk produces a
// score from the hit map the instrumented run wrote, and the row gates it
// against the baseline. Driven through `score` rather than by assigning the
// report, because the wiring from the language to the row is the whole subject.
func TestRustAndTypeScriptComponentsAreScoredAndGated(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		lang runner.Lang
		file string
		src  string
	}{
		{"rust", runner.Rust, "svc/src/lib.rs", tangledRust},
		{"typescript", runner.TypeScript, "web/src/a.ts", tangledTypeScript},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, c.file, c.src)
			m := measured(c.name, c.lang, 0, 12)
			m.Hits = untested(c.file, 12)
			m.CRAP, m.CRAPWhy = score(root, m)
			if !m.Scored() {
				t.Fatalf("score(%s) = (%+v, %q), want a report", c.file, m.CRAP, m.CRAPWhy)
			}

			// With nothing to compare against, the component is new and gates
			// nothing — the state every repository is in the first time it runs
			// a lydite that scores this language.
			fresh, findings := crapRow(m, nil, true)
			if fresh.Status != ui.StatusNew || !strings.Contains(fresh.Value, "no baseline yet") {
				t.Errorf("crap(%s) with no baseline = %+v, want new", c.name, fresh)
			}
			if !strings.Contains(fresh.Value, "1 function(s) above 30") {
				t.Errorf("crap(%s) = %q, want the tangled function counted", c.name, fresh.Value)
			}
			if len(findings) != 0 {
				t.Errorf("crap(%s) claimed %v on a row that gates nothing", c.name, findings)
			}

			// And above its baseline count it fails, naming the function to act
			// on — the same claim a Go component makes.
			row, findings := crapRow(m, gitstate.CRAPBaseline{c.name: {Above: 0}}, true)
			if row.Status != ui.StatusFail || !strings.Contains(row.Value, "1 more") {
				t.Fatalf("crap(%s) = %+v, want a failure over the delta", c.name, row)
			}
			if len(findings) != 1 || findings[0].Site != "tangled" || findings[0].Path != c.file {
				t.Errorf("findings = %+v, want the tangled function located", findings)
			}
		})
	}
}

// A component whose every scorable function is declared is not clean; it is a
// component nothing was scored in, and it reads the way a Go component in that
// state does — amber, and never the green of a repository with no debt.
func TestAnAllExcludedRustComponentIsNotAPass(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "svc/src/lib.rs", `// tangled is untested and full of branches.
// [lydite:exclude_from_crap][the proving ground exercises this end to end]
pub fn tangled(a: i64, b: i64) -> i64 {
    let mut n = 0;
    if a > 0 { n += 1; }
    if b > 0 { n += 1; }
    n
}
`)
	m := measured("svc", runner.Rust, 0, 8)
	m.Hits = untested("svc/src/lib.rs", 8)
	m.CRAP, m.CRAPWhy = score(root, m)
	if m.Scored() {
		t.Fatalf("score = %+v, want nothing scored — every function is declared", m.CRAP)
	}
	if m.CRAP.Excluded != 1 || !strings.Contains(m.CRAPWhy, "every function") {
		t.Errorf("score = (%+v, %q), want the declared function counted and named", m.CRAP, m.CRAPWhy)
	}
	row, _ := crapRow(m, nil, true)
	if row.Status != ui.StatusUnmeasured {
		t.Errorf("crap(svc) = %+v, want amber — a gate that scored nothing must not read as one that passed", row)
	}
}

// A component whose coverage report describes no function is amber too, in
// every language: a gate with nothing to measure is not a gate that passed.
func TestATypeScriptComponentWithNoFunctionToScoreIsNotAPass(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "web/src/a.ts", "export const answer = 42;\n")
	m := measured("web", runner.TypeScript, 0, 1)
	m.Hits = untested("web/src/a.ts", 1)
	m.CRAP, m.CRAPWhy = score(root, m)
	if m.Scored() || !strings.Contains(m.CRAPWhy, "no function to score") {
		t.Fatalf("score = (%+v, %q), want the report to say it describes no function", m.CRAP, m.CRAPWhy)
	}
	if row, _ := crapRow(m, nil, true); row.Status != ui.StatusUnmeasured {
		t.Errorf("crap(web) = %+v, want amber rather than a silent pass", row)
	}
}

// A TypeScript component mixing a .ts file with a .jsx file scores the .ts
// file and names the .jsx one skipped, on both branches of that split: a
// still-scored component says so in its value rather than reading as fully
// scored, and a component that is nothing but skipped files reads amber for
// that specific reason rather than the generic "no function to score".
func TestASkippedFileIsNamedRatherThanSilentlyDropped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "web/src/plain.ts", "export function f(a: number): number {\n  return a\n}\n")
	write(t, root, "web/src/widget.jsx", "export function Widget() {\n  return 1\n}\n")
	m := measured("web", runner.TypeScript, 0, 6)
	m.Hits = coverage.LineHits{
		"web/src/plain.ts":   {1: 1, 2: 1, 3: 1},
		"web/src/widget.jsx": {1: 1, 2: 1, 3: 1},
	}
	m.CRAP, m.CRAPWhy = score(root, m)
	if !m.Scored() {
		t.Fatalf("score = (%+v, %q), want the .ts file scored", m.CRAP, m.CRAPWhy)
	}
	value := crapValue(m.CRAP)
	if !strings.Contains(value, "1 file(s) not walked") {
		t.Errorf("crapValue = %q, want it to name the skipped .jsx file", value)
	}
	if clean := crapValue(crap.Report{Scored: 1}); strings.Contains(clean, "not walked") {
		t.Errorf("crapValue = %q, want no clause when nothing was skipped", clean)
	}

	all := measured("web", runner.TypeScript, 0, 3)
	all.Hits = coverage.LineHits{"web/src/widget.jsx": {1: 1, 2: 1, 3: 1}}
	all.CRAP, all.CRAPWhy = score(root, all)
	if all.Scored() || !strings.Contains(all.CRAPWhy, "could not be walked") {
		t.Fatalf("score = (%+v, %q), want it to say every file could not be walked", all.CRAP, all.CRAPWhy)
	}
	if row, _ := crapRow(all, nil, true); row.Status != ui.StatusUnmeasured {
		t.Errorf("crap(web) = %+v, want amber rather than a silent pass", row)
	}
}

// The denominator is every component CRAP applies to, across all three
// languages. A repository reporting its Rust and TypeScript components as
// outside the metric would understate how much of itself the figure covers.
func TestTheSummaryDenominatorSpansEveryScoredLanguage(t *testing.T) {
	t.Parallel()
	rust := scored("svc", 1, 56.0)
	rust.Lang = runner.Rust
	web := scored("web", 2, 90.0)
	web.Lang = runner.TypeScript
	docs := unmeasurableComponent(component.Component{Name: "docs", Dir: "docs", Command: []string{"make"}},
		"the component declares a raw command, which has no instrumented variant")

	row, ok := crapSummaryOf([]measurement{scored("api", 3, 41.0), rust, web, docs}, nil, nil)
	if !ok {
		t.Fatal("no summary row over a repository with three scored components")
	}
	for _, want := range []string{"6 function(s) above 30", "3 of 3 component(s)", "worst 90.0"} {
		if !strings.Contains(row.Value, want) {
			t.Errorf("crap = %q, want it to say %q", row.Value, want)
		}
	}
}

// A declaration that documents no function reaches a reader. Its author
// believes they have answered a finding, and nothing they can see says
// otherwise — so the run that renders rows says which ones covered nothing, on
// the command's own stderr rather than the process's, since a base tree is
// measured through the same path and its report is discarded.
func TestADeclarationCoveringNoFunctionIsNamedOnTheCommandsStderr(t *testing.T) {
	t.Parallel()
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
	}}
	m := scored("api", 1, 41.5)
	m.CRAP.Unused = []string{"api/lib.go:12"}

	cmd := newTestCmd()
	var errOut strings.Builder
	cmd.SetErr(&errOut)
	addCoverageRows(context.Background(), cmd, ui.NewReport("test"), t.TempDir(), decl, decl.Components,
		[]measurement{m}, config.Default(), coverageOptions{Instrument: true})

	got := errOut.String()
	if !strings.Contains(got, "api/lib.go:12") {
		t.Errorf("stderr = %q, want the declaration located", got)
	}
	if !strings.Contains(got, annotation.Marker(annotation.CRAP)) {
		t.Errorf("stderr = %q, want it to name the token an author has to fix", got)
	}
}
