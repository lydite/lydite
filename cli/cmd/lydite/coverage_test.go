package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// lines is a measured count, for a test that cares about the ratio rather than
// about which report produced it.
func lines(covered, total int) coverage.LineCount {
	return coverage.LineCount{Covered: covered, Total: total}
}

// measured is a component that produced a measurement.
func measured(name string, lang runner.Lang, covered, total int) measurement {
	return measurement{Name: name, Dir: name, Lang: lang, Lines: lines(covered, total)}
}

// rowsOf renders a report to a label -> row map for assertions.
func rowsOf(rep *ui.Report) map[string]ui.Row {
	out := map[string]ui.Row{}
	for _, r := range rep.Rows() {
		out[r.Label] = r
	}
	return out
}

// A run that measured but did not gate must not render as a pass. A workflow
// that forgot --gate-coverage would otherwise report exactly the green a gated
// run reports, which is the wardnet/wardnet#957 failure one layer out: a gate
// that never ran, indistinguishable from one that passed.
func TestAnUngatedRunNeverRendersAsAPass(t *testing.T) {
	rep := ui.NewReport("test")
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
	}}
	ms := []measurement{measured("api", runner.Go, 9, 10)}
	addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl, ms, config.Default(),
		coverageOptions{Instrument: true})
	rows := rowsOf(rep)
	for _, label := range []string{"coverage(api)", "go coverage", "coverage"} {
		row, ok := rows[label]
		if !ok {
			t.Fatalf("no %q row; got %v", label, rep.Rows())
		}
		if row.Status == ui.StatusPass {
			t.Errorf("%s is a pass, but nothing was gated: %+v", label, row)
		}
		if row.Status != ui.StatusContext {
			t.Errorf("%s status = %q, want context", label, row.Status)
		}
	}
	// The verdict is still a pass: measuring without gating is not a failure,
	// it is a run that made no claim.
	if rep.ExitCode() != 0 {
		t.Errorf("exit = %d, want 0 — an ungated run gates nothing and fails nothing", rep.ExitCode())
	}
}

// --no-coverage emits no coverage row at all. A row saying `unmeasured` on
// every fast local run trains readers to ignore the tag that exists to be
// noticed; the state that must stay visible is measured-and-not-gated.
func TestNoCoverageEmitsNoRows(t *testing.T) {
	rep := ui.NewReport("test")
	decl := component.File{Components: []component.Component{{Name: "api", Dir: "api", Runner: runner.GoTest}}}
	addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl,
		[]measurement{unmeasuredComponent(decl.Components[0], "coverage is off for this run")},
		config.Default(), coverageOptions{})
	if len(rep.Rows()) != 0 {
		t.Errorf("rows = %v, want none", rep.Rows())
	}
}

// The three altitudes are sums over one stored quantity, so they cannot
// disagree — and the language figure weights by lines rather than averaging
// percentages. A 1000-line component at 90% and a 10-line one at 0% is 89.1%,
// not the 45% a mean would report.
func TestComposedFiguresAreLineWeighted(t *testing.T) {
	ms := []measurement{
		measured("big", runner.Go, 900, 1000),
		measured("tiny", runner.Go, 0, 10),
	}
	got, fresh, carried := composed(ms, nil, everything)
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
	current := []measurement{
		measured("api", runner.Go, 50, 100),
		{Name: "sdk", Dir: "sdk", Lang: runner.Go, Lines: lines(80, 100)},
	}
	baseline := gitstate.Baseline{"api": lines(50, 100), "sdk": lines(80, 100)}
	row := composedRow("go coverage", current, map[string]bool{"sdk": true}, baseline, byLang(runner.Go), 0.1)
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
	current := []measurement{
		measured("api", runner.Go, 50, 100),
		unmeasuredComponent(component.Component{Name: "sdk", Dir: "sdk", Runner: runner.GoTest}, "not affected"),
	}
	baseline := gitstate.Baseline{"api": lines(50, 100), "sdk": lines(5, 1000)}
	row := composedRow("go coverage", current, nil, baseline, byLang(runner.Go), 0.1)
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
	current := []measurement{
		measured("api", runner.Go, 50, 100),
		measured("sdk", runner.Go, 90, 100),
	}
	row := composedRow("go coverage", current, nil, gitstate.Baseline{"api": lines(50, 100)}, byLang(runner.Go), 0.1)
	if row.Status != ui.StatusNew {
		t.Errorf("row = %+v, want new — the baseline covers one of the two components", row)
	}
}

// A component's own baseline is what gates it, and a dip beyond the tolerance
// fails. The comparison is at display precision, so a component shown as
// holding steady is never failed for a difference the report cannot show.
func TestAComponentIsGatedAgainstItsOwnBaseline(t *testing.T) {
	base := gitstate.Baseline{"api": lines(80, 100)}
	for _, tc := range []struct {
		name    string
		covered int
		want    ui.Status
	}{
		{"a real dip fails", 70, ui.StatusFail},
		{"holding steady passes", 80, ui.StatusPass},
		{"an improvement passes", 90, ui.StatusPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := componentRow(measured("api", runner.Go, tc.covered, 100), base, 0.1)
			if row.Status != tc.want {
				t.Errorf("row = %+v, want %q", row, tc.want)
			}
		})
	}
	// A component the baseline has never seen is reported, not failed —
	// mirroring how a language with no baseline was always handled.
	if row := componentRow(measured("web", runner.TypeScript, 1, 10), base, 0.1); row.Status != ui.StatusNew {
		t.Errorf("row = %+v, want new", row)
	}
}

// A component that could not be measured names why, and says when the figures
// above it are carrying its baseline forward. A carried number that says
// nothing about itself is one a reader takes for a measurement.
func TestAnUnmeasuredComponentNamesWhyAndWhatIsCarried(t *testing.T) {
	c := component.Component{Name: "tally", Dir: "rust", Runner: runner.CargoNextest}
	base := gitstate.Baseline{"tally": lines(60, 100)}

	skipped := unmeasuredComponent(c, "the component was not selected for this run")
	skipped.Carryable = true
	row := componentRow(skipped, base, 0.1)
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured", row)
	}
	if !strings.Contains(row.Value, "not selected") {
		t.Errorf("value = %q, want it to name the reason", row.Value)
	}
	if !strings.Contains(row.Value, "60.0%") {
		t.Errorf("value = %q, want it to say the baseline is being carried forward", row.Value)
	}

	// A component that ran and failed carries nothing, so its row must not
	// claim a number is standing in for it.
	failed := unmeasuredComponent(c, "test(tally) did not pass: failed")
	if got := componentRow(failed, base, 0.1); strings.Contains(got.Value, "60.0%") {
		t.Errorf("value = %q — a failed component's old figure is a guess, not a stand-in", got.Value)
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
			m := unmeasuredComponent(decl.Components[0], "why")
			m.Carryable = tc.carryable
			baseline := gitstate.Baseline{"web": lines(80, 100)}
			current := []measurement{m}
			carried := map[string]bool{}
			if tc.carryable {
				current = []measurement{{Name: "web", Dir: "web", Lang: runner.TypeScript, Lines: baseline["web"]}}
				carried["web"] = true
			}
			row := composedRow("typescript coverage", current, carried, baseline, byLang(runner.TypeScript), 0.1)
			if row.Status != tc.want {
				t.Errorf("row = %+v, want %q", row, tc.want)
			}
		})
	}
}

// The floor gates every measured component and needs no baseline, which is why
// it applies whether or not the run reads one. An unmeasured component is
// named and never folded into the passing count: a gate that did not run must
// be visibly distinct from one that passed and from one that failed.
func TestTheFloorGatesEachComponentAndNamesWhatItSkipped(t *testing.T) {
	rep := ui.NewReport("test")
	floorRows(rep, []measurement{
		measured("api", runner.Go, 90, 100),
		measured("sdk", runner.Go, 10, 100),
		unmeasuredComponent(component.Component{Name: "web", Dir: "web", Runner: runner.Vitest}, "not affected"),
	}, 50)
	rows := rowsOf(rep)
	if got := rows["floor(sdk)"].Status; got != ui.StatusFail {
		t.Errorf("floor(sdk) = %q, want a failure at 10%% against a 50%% floor", got)
	}
	if _, ok := rows["floor(api)"]; ok {
		t.Errorf("a component clearing the floor produced a row: %+v", rows["floor(api)"])
	}
	if got := rows["floor(web)"].Status; got != ui.StatusUnmeasured {
		t.Errorf("floor(web) = %q, want unmeasured", got)
	}
	// With one component below the floor there is no summary line to
	// mistake for a repository-wide pass.
	if _, ok := rows["floor"]; ok {
		t.Errorf("a summary row appeared beside a failing component: %+v", rows["floor"])
	}
}

// The summary says N of M, because the two differ exactly when some component
// went ungated — so a partial run cannot read as a repository-wide pass.
func TestTheFloorSummarySaysHowManyItCovered(t *testing.T) {
	rep := ui.NewReport("test")
	floorRows(rep, []measurement{
		measured("api", runner.Go, 90, 100),
		unmeasuredComponent(component.Component{Name: "web", Dir: "web", Runner: runner.Vitest}, "not affected"),
	}, 50)
	if got := rowsOf(rep)["floor"].Value; !strings.Contains(got, "1 of 2 component(s)") {
		t.Errorf("floor summary = %q, want it to say one of two was gated", got)
	}
}

// A floor of 0 is off, which is the default: upgrading lydite must never start
// failing a repository over a gap it has always had.
func TestTheFloorIsOffByDefault(t *testing.T) {
	rep := ui.NewReport("test")
	floorRows(rep, []measurement{measured("api", runner.Go, 0, 100)}, config.Default().Coverage.Floor)
	if len(rep.Rows()) != 0 {
		t.Errorf("rows = %v, want none with the floor disabled", rep.Rows())
	}
}

// A diff is scoped to the files a component's own report could speak for:
// under its directory, in a language its runner implies. Without both halves a
// repository with a Go and a TypeScript component over one root would score
// each against the other's changed files.
func TestChangedLinesAreScopedToTheComponent(t *testing.T) {
	changed := map[string][]int{
		"go/api/main.go": {1, 2},
		"go/sdk/main.go": {3},
		"web/src/app.ts": {4},
		"go/api/README":  {5},
	}
	got := scopeToComponent(changed, measurement{Name: "api", Dir: "go/api", Lang: runner.Go})
	if len(got) != 1 || got["go/api/main.go"] == nil {
		t.Errorf("scoped = %v, want only go/api/main.go", got)
	}
	web := scopeToComponent(changed, measurement{Name: "web", Dir: "web", Lang: runner.TypeScript})
	if len(web) != 1 || web["web/src/app.ts"] == nil {
		t.Errorf("scoped = %v, want only web/src/app.ts", web)
	}
	// A component rooted at the scan root claims every path under it, which
	// is the right answer to "whose report could contain this file" — and is
	// deliberately not the answer affected selection gives to "was this path
	// understood".
	root := scopeToComponent(changed, measurement{Name: "root", Dir: ".", Lang: runner.Go})
	if len(root) != 2 {
		t.Errorf("scoped = %v, want both Go files", root)
	}
}

// Patch coverage gates against that component's own aggregate baseline. A
// component with no baseline is reported, not failed.
func TestPatchIsGatedAgainstTheComponentsOwnBaseline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hit, tot  int
		base      coverage.LineCount
		wantState ui.Status
	}{
		{"untested new code fails", 0, 4, lines(80, 100), ui.StatusFail},
		{"well tested new code passes", 4, 4, lines(80, 100), ui.StatusPass},
		{"no baseline is reported", 0, 4, coverage.LineCount{}, ui.StatusNew},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := patchRow("patch(api)", tc.hit, tc.tot, tc.base, 0.1); got.Status != tc.wantState {
				t.Errorf("row = %+v, want %q", got, tc.wantState)
			}
		})
	}
}

// A tolerated dip is recorded as the baseline's own value, so the tolerance
// cannot become an unbounded downward ratchet: each change dipping by less than
// the tolerance would otherwise lower what the next one is measured against,
// and coverage bleeds one tolerance per merge with every gate green.
func TestAToleratedDipDoesNotLowerTheBaseline(t *testing.T) {
	baseline := gitstate.Baseline{"api": lines(800, 1000), "web": lines(500, 1000)}
	// api dips 0.1pp — inside the tolerance. web drops 10pp, which failed
	// visibly on the change that introduced it, so it is recorded as measured.
	record := gitstate.Baseline{"api": lines(799, 1000), "web": lines(400, 1000)}
	got := withToleratedDipsRestored(record, baseline, 0.1)
	if got["api"] != baseline["api"] {
		t.Errorf("api = %+v, want the baseline's %+v restored", got["api"], baseline["api"])
	}
	if got["web"] != record["web"] {
		t.Errorf("web = %+v, want the measured %+v recorded", got["web"], record["web"])
	}
}

// Every declared component produces exactly one measurement, including one this
// invocation never selected. Omitting the rest would make a narrowed run
// indistinguishable from a complete one.
func TestEveryDeclaredComponentIsAccountedFor(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "tally", Dir: "rust", Runner: runner.CargoNextest},
		{Name: "api", Dir: "go/api", Runner: runner.GoTest},
		{Name: "web", Dir: "web", Runner: runner.Vitest},
	}}
	got := inDeclarationOrder(decl, []measurement{measured("api", runner.Go, 1, 2)}, true)
	if len(got) != 3 {
		t.Fatalf("got %d measurements, want one per declared component", len(got))
	}
	for i, want := range []string{"tally", "api", "web"} {
		if got[i].Name != want {
			t.Errorf("position %d is %q, want %q — declaration order", i, got[i].Name, want)
		}
	}
	if got[0].Why == "" || got[2].Why == "" {
		t.Errorf("an unselected component produced no reason: %+v", got)
	}
	// The language comes from the declaration, so a component that never ran
	// still lands in its language's figure as unmeasured rather than in none.
	if got[0].Lang != runner.Rust || got[2].Lang != runner.TypeScript {
		t.Errorf("languages = %q, %q; want rust and typescript", got[0].Lang, got[2].Lang)
	}
}

// A component declaring a raw command has no instrumented variant to ask for,
// and there is deliberately no key naming where its coverage lands. It is
// unmeasured with the reason said out loud, never excluded: excluding it drops
// it from the composed figures silently, and a gate covering fewer components
// than the repository has would read as a complete one.
func TestARawCommandComponentIsUnmeasuredAndNotExcluded(t *testing.T) {
	c := component.Component{Name: "docs", Dir: "docs", Command: []string{"make", "check"}}
	m := measure(context.Background(), t.TempDir(), c, runner.Invocation{Name: "make"}, true)
	if m.Measured() {
		t.Fatal("a raw command produced a measurement")
	}
	if !strings.Contains(m.Why, "raw command") {
		t.Errorf("why = %q, want it to name the raw command", m.Why)
	}
	if m.Name != "docs" {
		t.Errorf("name = %q — the component must still be accounted for", m.Name)
	}
}

// A report lydite cannot read leaves the component unmeasured with the error,
// never with a zero measurement. A component measured at 0% and one whose
// report is missing read identically as a percentage and mean opposite things.
func TestAMissingReportIsUnmeasuredNotZero(t *testing.T) {
	c := component.Component{Name: "api", Dir: "api", Runner: runner.GoTest}
	m := measure(context.Background(), t.TempDir(), c, runner.Invocation{CoverageReport: ".lydite-reports/coverage.out"}, true)
	if m.Measured() {
		t.Fatal("a missing report produced a measurement")
	}
	if m.Lines.Total != 0 || m.Lines.Covered != 0 {
		t.Errorf("lines = %+v, want nothing", m.Lines)
	}
	if m.Why == "" {
		t.Error("no reason given for an unreadable report")
	}
}

// The whole point of measuring: a component's report is read back into the
// counts the baseline stores, from the artefact its own instrumented run wrote.
func TestMeasureReadsTheReportTheInvocationNamed(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"api/go.mod":  "module example.com/api\n\ngo 1.26\n",
		"api/main.go": "package api\n\nfunc F() int {\n\treturn 1\n}\n",
		"api/.lydite-reports/coverage/coverage.out": "mode: set\nexample.com/api/main.go:3.16,5.2 1 1\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := component.Component{Name: "api", Dir: "api", Runner: runner.GoTest}
	inv, err := invocation(c, runner.Instrumented)
	if err != nil {
		t.Fatal(err)
	}
	m := measure(context.Background(), root, c, inv, true)
	if !m.Measured() {
		t.Fatalf("unmeasured: %s", m.Why)
	}
	if m.Lines != lines(1, 1) {
		t.Errorf("lines = %+v, want {1 1}", m.Lines)
	}
	if _, ok := m.Hits["api/main.go"]; !ok {
		t.Errorf("hits = %v, want a scan-root-relative key", m.Hits)
	}
}

// The instrumented variant is what a run measures from, so the report path the
// gate reads has to be the one the invocation actually writes. A test asserting
// the two separately passes while they disagree.
func TestTheMeasuredPathIsTheOneTheInvocationWrites(t *testing.T) {
	for _, name := range []runner.Name{runner.GoTest, runner.CargoNextest, runner.Vitest, runner.Jest} {
		inv, err := invocation(component.Component{Name: "c", Dir: "c", Runner: name}, runner.Instrumented)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if inv.CoverageReport == "" {
			t.Errorf("%s's instrumented variant names no coverage report", name)
			continue
		}
		joined := strings.Join(inv.Args, " ")
		if !strings.Contains(joined, inv.CoverageReport) && !strings.Contains(joined, filepath.Dir(inv.CoverageReport)) {
			t.Errorf("%s: CoverageReport %q is nowhere in %q", name, inv.CoverageReport, joined)
		}
	}
}

// --no-coverage and --gate-coverage together have no answer to give: there is
// nothing measured for the gate to compare. Silently honouring one of the two
// would leave a workflow believing it gates.
func TestNoCoverageAndGateCoverageAreRefusedTogether(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"test", "--dir", t.TempDir(), "--no-coverage", "--gate-coverage"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("the two flags were accepted together")
	}
	if !strings.Contains(err.Error(), "--no-coverage") || !strings.Contains(err.Error(), "--gate-coverage") {
		t.Errorf("error %q does not name both flags", err)
	}
}

// `lydite coverage` is gone, and says so. Cobra's unknown-command answer leaves
// a consumer to guess whether it was renamed, dropped, or never existed — the
// same guess a silently ignored config key produces, one layer out.
func TestTheRemovedCoverageCommandNamesWhatReplacedIt(t *testing.T) {
	for _, args := range [][]string{
		{"coverage"},
		{"coverage", "--source=report"},
		{"coverage", "--go-report", "coverage.out"},
	} {
		cmd := newRootCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&strings.Builder{})
		cmd.SetErr(&strings.Builder{})
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("%v was accepted", args)
		}
		for _, want := range []string{"lydite test", "--gate-coverage"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: error %q does not name %q", args, err, want)
			}
		}
	}
}

// A component the base tree had and this one no longer declares is gone from
// the recorded baseline, rather than carried forever as an entry nobody can
// measure.
func TestARemovedComponentLeavesTheBaseline(t *testing.T) {
	decl := component.File{Components: []component.Component{{Name: "api", Dir: "api", Runner: runner.GoTest}}}
	ms := inDeclarationOrder(decl, nil, true)
	baseline := gitstate.Baseline{"api": lines(50, 100), "gone": lines(90, 100)}
	// recordThisTree writes through git, so the assertion is on the shape it
	// builds: every declared component present, and nothing else.
	record := gitstate.Baseline{}
	for _, m := range ms {
		if b, ok := baseline[m.Name]; ok {
			record[m.Name] = b
		}
	}
	if _, ok := record["gone"]; ok {
		t.Error("a component the declaration no longer has kept its baseline entry")
	}
	if len(record) != 1 {
		t.Errorf("record = %v, want only the declared component", record)
	}
}

// A language figure covers only its own components, so a repository's Rust
// coverage is never diluted by its Go.
func TestALanguageFigureCoversOnlyItsOwnComponents(t *testing.T) {
	ms := []measurement{
		measured("api", runner.Go, 90, 100),
		measured("tally", runner.Rust, 10, 100),
	}
	goLines, _, _ := composed(ms, nil, byLang(runner.Go))
	rustLines, _, _ := composed(ms, nil, byLang(runner.Rust))
	if goLines != lines(90, 100) || rustLines != lines(10, 100) {
		t.Fatalf("go = %+v, rust = %+v", goLines, rustLines)
	}
	all, _, _ := composed(ms, nil, everything)
	if all != lines(100, 200) {
		t.Errorf("global = %+v, want the sum of both", all)
	}
	if got := fmt.Sprint(languages(ms)); got != "[go rust]" {
		t.Errorf("languages = %s, want a stable sorted order", got)
	}
}

// A figure no component contributed to is unmeasured, never a 0.0% row. 0/0
// renders as 0.0%, which reads as a real measurement of no coverage at all: a
// language whose only component failed would report the worst possible number
// as though it had been measured, and a reader acting on it would go looking
// for missing tests rather than for the failing suite.
//
// Found on the proving ground, where the `web` component fails for want of a
// coverage provider and its language reported `0.0% (0/0 lines)`.
func TestALanguageThatMeasuredNothingIsUnmeasured(t *testing.T) {
	rep := ui.NewReport("test")
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
		{Name: "web", Dir: "web", Runner: runner.Vitest},
	}}
	ms := []measurement{
		measured("api", runner.Go, 9, 10),
		unmeasuredComponent(decl.Components[1], "test(web) did not pass: failed"),
	}
	addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl, ms, config.Default(),
		coverageOptions{Instrument: true})
	rows := rowsOf(rep)
	ts, ok := rows["typescript coverage"]
	if !ok {
		t.Fatalf("no typescript row; got %v", rep.Rows())
	}
	if ts.Status != ui.StatusUnmeasured {
		t.Errorf("typescript coverage = %+v, want unmeasured rather than a figure", ts)
	}
	if strings.Contains(ts.Value, "0.0%") {
		t.Errorf("typescript coverage value = %q, want no percentage at all", ts.Value)
	}
	// The language that did measure is unaffected, and so is the global
	// figure — which still has something in it.
	if rows["go coverage"].Status != ui.StatusContext {
		t.Errorf("go coverage = %+v, want a measured-but-ungated row", rows["go coverage"])
	}
	if rows["coverage"].Status != ui.StatusContext {
		t.Errorf("global coverage = %+v, want a measured-but-ungated row", rows["coverage"])
	}
}

// With nothing measured at all, the global figure says so too — a repository
// whose every component failed must not report 0.0% coverage.
func TestAGlobalFigureThatMeasuredNothingIsUnmeasured(t *testing.T) {
	rep := ui.NewReport("test")
	decl := component.File{Components: []component.Component{{Name: "api", Dir: "api", Runner: runner.GoTest}}}
	ms := []measurement{unmeasuredComponent(decl.Components[0], "test(api) did not pass: failed")}
	addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl, ms, config.Default(),
		coverageOptions{Instrument: true})
	if got := rowsOf(rep)["coverage"]; got.Status != ui.StatusUnmeasured {
		t.Errorf("coverage = %+v, want unmeasured", got)
	}
}

// The report a run is measured from has to have been written by that run.
// A report left behind by an earlier one is what a suite that passes without
// writing one gets measured from — a coverage number describing code that is
// no longer there, supplied by lydite itself.
//
// Asserted on clearReport rather than through a component run: every runner
// lydite ships truncates its own report, so an end-to-end test passes with the
// guarantee removed and establishes nothing. What is asserted is the property
// the measurement rests on.
func TestTheReportPathIsClearedBeforeTheSuiteRuns(t *testing.T) {
	dir := t.TempDir()
	report := ".lydite-reports/coverage/coverage.out"
	path := filepath.Join(dir, filepath.FromSlash(report))

	// The directory does not exist yet, which is the fresh-checkout case.
	if err := clearReport(dir, report); err != nil {
		t.Fatalf("clearReport on a missing directory: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("the report directory was not created: %v", err)
	}

	// A report from an earlier run is gone afterwards.
	if err := os.WriteFile(path, []byte("mode: set\nexample.com/svc/gone.go:1.1,2.2 9 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearReport(dir, report); err != nil {
		t.Fatalf("clearReport over an existing report: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the previous run's report is still there (%v), so a suite that writes none would be measured from it", err)
	}
}

// On the default branch HEAD is its own merge-base, so the tree this run just
// measured is the tree a baseline would be read for. Reading it would miss on
// the first build and measure the whole repository a second time, in a
// throwaway worktree, to reproduce numbers already in hand.
//
// There is nothing to compare against either, so the figures render as an
// ungated run's do. Rendering them as passes would claim a comparison that
// never happened.
func TestGatingOnTheDefaultBranchRecordsRatherThanRemeasures(t *testing.T) {
	root := t.TempDir()
	origin := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run(origin, "init", "--bare", "-b", "main", ".")
	run(root, "init", "-b", "main", ".")
	run(root, "config", "user.email", "t@t")
	run(root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "-A")
	run(root, "commit", "-m", "seed")
	run(root, "remote", "add", "origin", origin)
	run(root, "push", "-u", "origin", "main")

	decl := component.File{Components: []component.Component{{Name: "api", Dir: ".", Runner: runner.GoTest}}}
	ms := []measurement{measured("api", runner.Go, 9, 10)}
	rep := ui.NewReport("test")
	cmd := newTestCmd()
	cmd.SetErr(&strings.Builder{})
	addCoverageRows(context.Background(), cmd, rep, root, decl, ms, config.Default(),
		coverageOptions{Instrument: true, Gate: true, Concurrency: 1})
	rows := rowsOf(rep)
	base, ok := rows["baseline"]
	if !ok {
		t.Fatalf("no baseline row; got %v", rep.Rows())
	}
	if strings.Contains(base.Value, "measuring it now") {
		t.Errorf("baseline row = %q — the tree this run measured was measured again", base.Value)
	}
	if got := rows["coverage(api)"].Status; got != ui.StatusContext {
		t.Errorf("coverage(api) = %q, want a context row: nothing was compared", got)
	}
}

// Only affected selection licenses carrying an unmeasured component forward.
// It determined the change could not have broken that component, so it is
// unchanged from the tree the baseline entry describes.
//
// `--component` narrows for an unrelated reason — the caller wanted these
// components run — and says nothing about the others. Carrying under it
// attributes the merge-base's number to a component this very change may have
// rewritten, which is the failure Carryable exists to prevent one case of.
func TestOnlyAffectedSelectionMakesAComponentCarryable(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "go/api", Runner: runner.GoTest},
		{Name: "web", Dir: "web", Runner: runner.Vitest},
	}}
	ran := []measurement{measured("api", runner.Go, 9, 10)}
	for _, tc := range []struct {
		name     string
		selected bool
		want     bool
	}{
		{"--affected narrowed the run", true, true},
		{"--component narrowed the run", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inDeclarationOrder(decl, ran, tc.selected)
			var web measurement
			for _, m := range got {
				if m.Name == "web" {
					web = m
				}
			}
			if web.Carryable != tc.want {
				t.Errorf("web.Carryable = %v, want %v", web.Carryable, tc.want)
			}
		})
	}
}

// A baseline read that cannot happen fails through a row, never by discarding
// the report. Everything here runs after every suite has finished, so a
// returned error throws away component rows, log paths and the whole `--json`
// document for a failure about the gate rather than about the code.
//
// A failing row and not an unmeasured one: the caller passed --gate-coverage,
// and a gate that was asked for and silently did not run is the state this
// whole file exists to make impossible.
func TestAGateThatCouldNotRunFailsThroughARowAndKeepsTheReport(t *testing.T) {
	// No git repository at all, so the merge-base cannot be resolved.
	root := t.TempDir()
	rep := ui.NewReport("test")
	cmd := newTestCmd()
	cmd.SetErr(&strings.Builder{})
	decl := component.File{Components: []component.Component{{Name: "api", Dir: "api", Runner: runner.GoTest}}}
	ms := []measurement{measured("api", runner.Go, 9, 10)}

	addCoverageRows(context.Background(), cmd, rep, root, decl, ms, config.Default(),
		coverageOptions{Instrument: true, Gate: true, Concurrency: 1})

	rows := rowsOf(rep)
	base, ok := rows["baseline"]
	if !ok {
		t.Fatalf("no baseline row; got %v", rep.Rows())
	}
	if base.Status != ui.StatusFail {
		t.Errorf("baseline = %+v, want a failure — the gate was asked for and could not run", base)
	}
	if len(base.Detail) == 0 {
		t.Error("the failing row carries no reason")
	}
	// The measurement survives, because it is what a reader needs in order to
	// act on the gate that could not run.
	if _, ok := rows["coverage(api)"]; !ok {
		t.Errorf("the component's measurement was discarded: %v", rep.Rows())
	}
	if rep.ExitCode() == 0 {
		t.Error("exit = 0 for a run whose gate never ran")
	}
}

// Recording merges onto whatever the tree already holds rather than skipping
// because it holds something. A shard runs `--component` over part of the
// declaration, so the first to finish would otherwise be the only one that
// ever records, and every later shard's freshly measured components would be
// silently discarded.
func TestRecordingMergesRatherThanSkipping(t *testing.T) {
	existing := gitstate.Baseline{"api": lines(50, 100), "gone": lines(90, 100)}
	fresh := gitstate.Baseline{"web": lines(70, 100)}
	declared := map[string]bool{"api": true, "web": true}

	merged := gitstate.Baseline{}
	for name, l := range existing {
		if declared[name] {
			merged[name] = l
		}
	}
	for name, l := range fresh {
		merged[name] = l
	}
	if len(merged) != 2 || merged["api"] != lines(50, 100) || merged["web"] != lines(70, 100) {
		t.Errorf("merged = %v, want the earlier shard's api beside this one's web", merged)
	}
	// An entry for a component the declaration no longer holds does not
	// survive the merge.
	if _, ok := merged["gone"]; ok {
		t.Error("a component the declaration no longer has kept its entry")
	}
	// A merge that changes nothing must not push.
	if !sameCounts(merged, merged) {
		t.Error("sameCounts says an identical baseline differs")
	}
	if sameCounts(merged, existing) {
		t.Error("sameCounts says a changed baseline is identical")
	}
}

// A gate that fails partway through must not leave two rows under one label.
// gatedRows adds rows as it goes and can fail after most of them are in — a
// failing `git diff` inside patchRows is the live path — so the recovery
// rendering would otherwise put a gated `pass` and a `context` "not compared"
// under `coverage(api)`, and a consumer keying rows by label picks one of two
// contradictory answers.
func TestAFailedGateLeavesNoDuplicateRows(t *testing.T) {
	// No git repository, so the merge-base resolution fails before any gated
	// row is written; the assertion is about the labels either way.
	rep := ui.NewReport("test")
	cmd := newTestCmd()
	cmd.SetErr(&strings.Builder{})
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
		{Name: "web", Dir: "web", Runner: runner.Vitest},
	}}
	ms := []measurement{measured("api", runner.Go, 9, 10), measured("web", runner.TypeScript, 1, 10)}
	cfg := config.Default()
	cfg.Coverage.Floor = 50

	addCoverageRows(context.Background(), cmd, rep, t.TempDir(), decl, ms, cfg,
		coverageOptions{Instrument: true, Gate: true, Concurrency: 1})

	seen := map[string]int{}
	for _, r := range rep.Rows() {
		seen[r.Label]++
	}
	for label, n := range seen {
		if n > 1 {
			t.Errorf("label %q appears %d times — a consumer keying rows by label gets one of two answers", label, n)
		}
	}
}

// Anchoring a within-tolerance dip to the high-water mark must anchor the
// ratio, not the size. Writing the baseline's own counts back freezes the
// component's weight: one that grew from 1,000 lines to 2,000 while dipping
// inside the tolerance would be recorded as 1,000 lines, and the language and
// global baselines are sums of exactly these counts — so a stale weight decides
// how much the component counts towards a figure describing a tree it no
// longer matches.
func TestARestoredDipKeepsThisTreesSize(t *testing.T) {
	baseline := gitstate.Baseline{"api": lines(800, 1000)}
	// The component doubled in size and dipped 0.05pp, which is inside the
	// tolerance.
	record := gitstate.Baseline{"api": lines(1599, 2000)}
	got := withToleratedDipsRestored(record, baseline, 0.1)["api"]
	if got.Total != 2000 {
		t.Errorf("total = %d, want this tree's 2000 rather than the baseline's 1000", got.Total)
	}
	if pct := got.Percent(); pct < 79.95 || pct > 80.05 {
		t.Errorf("percent = %v, want the baseline's 80%% anchored over the new size", pct)
	}
}

// A run that could not measure a component it was supposed to has not
// established this tree's baseline. Recording a partial one is worse than
// recording none: any non-empty entry reads as a cache hit, so the missing
// component is `new` on every later change — and a composed figure refuses to
// compare unless the baseline covers every component in it, so that language's
// row and the global row stop gating too.
//
// A component nothing could ever measure does not block it: it contributes to
// neither side of any comparison, so its absence is permanent and expected.
func TestAPartialRunDoesNotRecordAPartialBaseline(t *testing.T) {
	for _, tc := range []struct {
		name       string
		gap        measurement
		wantRecord bool
	}{
		{"a component whose suite failed", unmeasuredComponent(
			component.Component{Name: "web", Dir: "web", Runner: runner.Vitest},
			"test(web) did not pass: failed"), false},
		{"a component nothing could measure", unmeasurableComponent(
			component.Component{Name: "docs", Dir: "docs", Command: []string{"make", "check"}},
			"the component declares a raw command, which has no instrumented variant"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The record holds only what was measured, which is what the
			// predicate reads.
			record := gitstate.Baseline{"api": lines(9, 10)}
			gap, blocked := recordingBlockedBy([]measurement{measured("api", runner.Go, 9, 10), tc.gap}, record)
			if blocked == tc.wantRecord {
				t.Errorf("recordingBlockedBy = (%v, %v), want blocked = %v", gap.Name, blocked, !tc.wantRecord)
			}
		})
	}
}

// A component affected selection deselected carries forward rather than
// blocking the recording: selection determined the change could not have
// broken it, so the baseline entry still describes it.
func TestADeselectedComponentDoesNotBlockRecording(t *testing.T) {
	skipped := unmeasuredComponent(component.Component{Name: "web", Dir: "web", Runner: runner.Vitest}, "not selected")
	skipped.Carryable = true
	ms := []measurement{measured("api", runner.Go, 9, 10), skipped}

	// It carried, so the record holds an entry for it.
	carried := gitstate.Baseline{"api": lines(9, 10), "web": lines(5, 10)}
	if gap, blocked := recordingBlockedBy(ms, carried); blocked {
		t.Errorf("recording blocked by %q, but selection determined it could not have been broken", gap.Name)
	}

	// It was entitled to carry and had nothing to carry, which is a different
	// thing. Recording anyway writes the same gap forward on every merge, and
	// it can then never heal: each run reproduces it from the last.
	if _, blocked := recordingBlockedBy(ms, gitstate.Baseline{"api": lines(9, 10)}); !blocked {
		t.Error("a deselected component with no baseline entry did not block recording")
	}
}

// lydite writes into the repository it is measuring, and what it writes must
// never become part of what it measures. A committed `.lydite-reports/` lands
// in the diff, where it matches no component and therefore widens affected
// selection to everything on every change — observed on a fixture whose select
// row named a coverage profile and a test log as the reason a second component
// ran.
func TestTheReportDirectoryDisownsItself(t *testing.T) {
	root := t.TempDir()
	reports := filepath.Join(root, runner.ReportDir)
	if err := os.MkdirAll(reports, 0o750); err != nil {
		t.Fatal(err)
	}
	ignoreReports(reports)

	data, err := os.ReadFile(filepath.Join(reports, ".gitignore"))
	if err != nil {
		t.Fatalf("no .gitignore in the report directory: %v", err)
	}
	if strings.TrimSpace(string(data)) != "*" {
		t.Errorf("ignore file = %q, want everything ignored", data)
	}

	// A repository that already wrote its own rules there keeps them: this
	// directory is lydite's, but the file is whoever wrote it first's.
	own := filepath.Join(reports, ".gitignore")
	if err := os.WriteFile(own, []byte("!keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignoreReports(reports)
	data, _ = os.ReadFile(own)
	if strings.TrimSpace(string(data)) != "!keep" {
		t.Errorf("ignore file = %q, want the existing one untouched", data)
	}
}

// A worktree holds the whole repository, and the scan root may sit below it. A
// base-tree measurement rooted at the worktree instead looks for the component
// declaration where there is none, finds no components, and hands back an
// empty baseline — so every component reads as new, every composed row refuses
// to compare, and the gate passes having compared nothing.
//
// A monorepo run as `--dir source` is the shape, and it is the one
// `ChangedLines` and `selectAffected` already account for. Asserted through
// `gitdiff.Prefix`, which is the mapping, rather than by measuring a base tree:
// what can be wrong here is which directory is treated as the scan root.
func TestTheBaseTreeIsMeasuredAtTheScanRootNotTheWorktreeRoot(t *testing.T) {
	repo := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run(repo, "init", "-b", "main", ".")
	scanRoot := filepath.Join(repo, "source")
	if err := os.MkdirAll(filepath.Join(scanRoot, ".lydite"), 0o750); err != nil {
		t.Fatal(err)
	}

	prefix, err := gitdiff.Prefix(context.Background(), scanRoot)
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "source/" {
		t.Fatalf("prefix = %q, want %q", prefix, "source/")
	}
	// The join is what measureBaseTree does with a worktree path, and it has
	// to land on the scan root's copy rather than on the repository root.
	worktree := t.TempDir()
	if got, want := filepath.Join(worktree, filepath.FromSlash(prefix)), filepath.Join(worktree, "source"); got != want {
		t.Errorf("base scan root = %q, want %q", got, want)
	}
	// At the repository root the mapping is the identity, so the common case
	// is unchanged.
	rootPrefix, err := gitdiff.Prefix(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if rootPrefix != "" {
		t.Errorf("prefix at the repository root = %q, want empty", rootPrefix)
	}
}

// A base tree lydite could not measure completely is not a baseline. A bare
// worktree is where a measurement most often fails — no container runtime for
// a component's services, an install that fails there — and a partial entry,
// once cached, reads as a hit for every later change: the missing component is
// new forever, and its language's row and the global row refuse to compare and
// stop gating, with a green verdict.
func TestAPartiallyMeasuredBaseTreeIsNotCached(t *testing.T) {
	// The predicate the base-tree path applies, over the measurements it
	// collected. A component nothing could ever measure is the one exemption.
	for _, tc := range []struct {
		name string
		ms   []measurement
		want bool
	}{
		{"every component measured", []measurement{
			measured("api", runner.Go, 9, 10),
		}, false},
		{"one component failed there", []measurement{
			measured("api", runner.Go, 9, 10),
			unmeasuredComponent(component.Component{Name: "web", Dir: "web", Runner: runner.Vitest}, "no container runtime"),
		}, true},
		{"a component nothing could measure", []measurement{
			measured("api", runner.Go, 9, 10),
			unmeasurableComponent(component.Component{Name: "docs", Dir: "docs", Command: []string{"make"}}, "raw command"),
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// What measureBaseTree collected, and what came of it — the same
			// two things a run hands the predicate when it records.
			out := gitstate.Baseline{}
			for _, m := range tc.ms {
				if m.Measured() {
					out[m.Name] = m.Lines
				}
			}
			gap, blocked := recordingBlockedBy(tc.ms, out)
			if blocked != tc.want {
				t.Errorf("recordingBlockedBy = (%q, %v), want blocked = %v", gap.Name, blocked, tc.want)
			}
		})
	}
}

// A tolerated dip is anchored against what this tree already holds, not only
// against the base baseline. A pull request records the anchored high-water
// entry for its tree; a squash merge lands a commit carrying that same tree; a
// run on the default branch measures it again and would otherwise replace the
// anchor with a raw dipped number, handing the next change a lowered figure to
// gate against — the per-merge ratchet the anchoring exists to prevent,
// reintroduced one path over.
func TestARerunOfOneTreeDoesNotLowerItsAnchoredEntry(t *testing.T) {
	anchored := gitstate.Baseline{"api": lines(800, 1000)}
	// The same tree measured again, 0.05pp lower: noise, which is what the
	// tolerance is for.
	remeasured := gitstate.Baseline{"api": lines(799, 1000)}
	got := withToleratedDipsRestored(remeasured, anchored, 0.1)
	if pct := got["api"].Percent(); pct < 79.95 || pct > 80.05 {
		t.Errorf("api = %v%%, want the anchored 80%% kept", pct)
	}
	// A drop beyond the tolerance is recorded: it failed visibly on the change
	// that introduced it, so accepting it is deliberate.
	dropped := withToleratedDipsRestored(gitstate.Baseline{"api": lines(700, 1000)}, anchored, 0.1)
	if dropped["api"] != lines(700, 1000) {
		t.Errorf("api = %v, want the measured drop recorded", dropped["api"])
	}
}

// The base tree is read leniently, because it is being measured rather than
// configuring lydite. The base tree of the pull request that removes a retired
// key is by construction the tree that still carries it, and the metric version
// bump makes that run a cache miss — so validating it would fail every
// migration, and keep failing every branch cut before the removal landed.
func TestAHistoricalConfigIsReadLenientlyAndACurrentOneIsNot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".lydite"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Exactly what a pre-upgrade tree carries.
	stale := "coverage:\n  source: report\n  go:\n    report: coverage.out\ntypescript:\n  install: \"yarn install\"\n"
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(dir); err == nil {
		t.Error("Load accepted a retired key — the rejection is what tells an author their config is stale")
	}
	got, err := config.LoadHistorical(dir)
	if err != nil {
		t.Fatalf("LoadHistorical refused the tree it exists to read: %v", err)
	}
	// What it is read for still arrives.
	if got.TypeScript.Install != "yarn install" {
		t.Errorf("TypeScript.Install = %q, want the historical tree's own value", got.TypeScript.Install)
	}
	// A file that is not YAML at all is still an error: there is nothing to
	// read there.
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("coverage:\n\tsource: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadHistorical(dir); err == nil {
		t.Error("LoadHistorical accepted a file that is not YAML")
	}
}

// A floor that cleared nothing examined nothing, whatever the count reads like.
// Without this a run where every component's report was unreadable renders
// `✓ floor … 0 of 4 component(s) at or above 80.0%` — a tick on a gate that
// looked at no component at all.
func TestAFloorThatClearedNothingIsNotAPass(t *testing.T) {
	rep := ui.NewReport("test")
	floorRows(rep, []measurement{
		unmeasuredComponent(component.Component{Name: "api", Dir: "api", Runner: runner.GoTest}, "no report"),
		unmeasuredComponent(component.Component{Name: "web", Dir: "web", Runner: runner.Vitest}, "no report"),
	}, 80)
	got := rowsOf(rep)["floor"]
	if got.Status == ui.StatusPass {
		t.Errorf("floor = %+v, want no pass — it was applied to nothing", got)
	}
	if got.Status != ui.StatusUnmeasured {
		t.Errorf("floor = %+v, want unmeasured", got)
	}
}

// `go list -m` run inside a module answers with the enclosing module, so a
// component whose dir is not a module root would take the root module's path —
// and goRelPath then strips that path and puts the component's dir back on,
// keying every profile entry `services/api/services/api/x.go`. Nothing
// downstream notices: the patch gate finds no overlap and emits no row, and the
// generated-file check stats a path that does not exist.
func TestAGoComponentBelowItsModuleRootIsRefused(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// One module at the repository root; the component points below it.
	write("go.mod", "module example.com/m\n\ngo 1.26\n")
	write("services/api/api.go", "package api\n\nfunc F() int { return 1 }\n")
	write("services/api/.lydite-reports/coverage/coverage.out",
		"mode: set\nexample.com/m/services/api/api.go:3.20,3.32 1 1\n")

	c := component.Component{Name: "api", Dir: "services/api", Runner: runner.GoTest}
	m := measure(context.Background(), root, c, runner.Invocation{CoverageReport: ".lydite-reports/coverage/coverage.out"}, true)
	if m.Measured() {
		t.Fatalf("measured %+v from a directory that is not a module root", m.Lines)
	}
	if !strings.Contains(m.Why, "module root") {
		t.Errorf("why = %q, want it to name what is wrong with the declaration", m.Why)
	}
}

// The two are different events in one run — the baseline read this change is
// gated against, and the entry it leaves for the next one — so they carry
// different labels. Sharing one puts two rows under it, which is what a
// consumer keying rows by label cannot survive.
func TestTheBaselineReadAndTheRecordAreDifferentRows(t *testing.T) {
	root := t.TempDir()
	origin := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run(origin, "init", "--bare", "-b", "main", ".")
	run(root, "init", "-b", "main", ".")
	run(root, "config", "user.email", "t@t")
	run(root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "-A")
	run(root, "commit", "-m", "seed")
	run(root, "remote", "add", "origin", origin)
	run(root, "push", "-u", "origin", "main")

	rep := ui.NewReport("test")
	cmd := newTestCmd()
	cmd.SetErr(&strings.Builder{})
	decl := component.File{Components: []component.Component{{Name: "api", Dir: ".", Runner: runner.GoTest}}}
	addCoverageRows(context.Background(), cmd, rep, root, decl,
		[]measurement{measured("api", runner.Go, 9, 10)}, config.Default(),
		coverageOptions{Instrument: true, Gate: true, Concurrency: 1})

	seen := map[string]int{}
	for _, r := range rep.Rows() {
		seen[r.Label]++
	}
	for label, n := range seen {
		if n > 1 {
			t.Errorf("label %q appears %d times", label, n)
		}
	}
	if seen["record"] == 0 {
		t.Errorf("no record row: a run that writes a baseline must say what it wrote; rows were %v", rep.Rows())
	}
}

// gateRepo is a repository with an origin, one Go component, and a base commit
// whose coverage is known. It exists so the gate can be exercised through the
// command rather than through its parts: the paths this drives —
// `baselineFor`, `measureBaseTree`, `patchRows`, `recordThisTree` — are where
// both of the severe defects found while building this lived, and neither was
// reachable from a unit test.
func gateRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run(origin, "init", "--bare", "-b", "main", ".")
	run(root, "init", "-b", "main", ".")
	run(root, "config", "user.email", "t@t")
	run(root, "config", "user.name", "t")

	write(t, root, component.FileName, "components:\n  - name: svc\n    dir: svc\n    runner: go-test\n    args: [\"./...\"]\n")
	write(t, root, "svc/go.mod", "module example.com/svc\n\ngo 1.26\n")
	// Two of four statements covered: Keep is exercised, Skip is not.
	write(t, root, "svc/lib.go", "package svc\n\n// Keep is exercised.\nfunc Keep(n int) int {\n\treturn n + 1\n}\n\n// Skip is not.\nfunc Skip(n int) int {\n\treturn n - 1\n}\n")
	write(t, root, "svc/lib_test.go", "package svc\n\nimport \"testing\"\n\nfunc TestKeep(t *testing.T) {\n\tif Keep(1) != 2 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	run(root, "add", "-A")
	run(root, "commit", "-m", "base")
	run(root, "remote", "add", "origin", origin)
	run(root, "push", "-u", "origin", "main")
	return root
}

// The whole gate, through the command, against a real repository: a cache miss
// measures the base tree, the comparison fails a change that lowers coverage,
// the patch gate scores the new lines, the measurement is recorded, and the
// next run hits the cache instead of measuring the base tree again.
//
// Driven end to end because that is the only way these paths run at all — and
// because both severe defects found while building this were in exactly this
// code: a base tree measured at the wrong root, and a base tree's config
// validated with rules it predates. Each produced a green gate that had
// compared nothing.
func TestTheGateAgainstARealRepository(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), root, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run("switch", "--quiet", "-c", "change")
	// A new function nobody tests: the aggregate falls and the patch gate has
	// a coverable new line to score.
	write(t, root, "svc/lib.go", "package svc\n\n// Keep is exercised.\nfunc Keep(n int) int {\n\treturn n + 1\n}\n\n// Skip is not.\nfunc Skip(n int) int {\n\treturn n - 1\n}\n\n// Added is not either.\nfunc Added(n int) int {\n\treturn n * 2\n}\n")
	run("add", "-A")
	run("commit", "-m", "add an untested function")

	out, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json")
	if err == nil {
		t.Fatalf("the gate passed a change that lowered coverage\nstdout: %s\nstderr: %s", out, errOut)
	}
	rows := jsonRows(t, out)

	// The base tree had no entry, so it was measured rather than substituted.
	if got := rows["baseline"]; !strings.Contains(got.Value, "measuring it now") {
		t.Errorf("baseline = %q, want a miss resolved by measuring", got.Value)
	}
	// The comparison happened, against a number that came from the base tree.
	comp := rows["coverage(svc)"]
	if comp.Status != "fail" {
		t.Errorf("coverage(svc) = %+v, want a failure", comp)
	}
	if !strings.Contains(comp.Value, "baseline") || !strings.Contains(comp.Value, "regressed") {
		t.Errorf("coverage(svc) = %q, want it to name the baseline and the regression", comp.Value)
	}
	// The patch gate scored the new lines rather than skipping in silence.
	patch := rows["patch(svc)"]
	if patch.Status != "fail" {
		t.Errorf("patch(svc) = %+v, want a failure — the new function has no test", patch)
	}
	if !strings.Contains(patch.Value, "new lines") {
		t.Errorf("patch(svc) = %q, want the changed-line counts", patch.Value)
	}
	// This tree's measurement was recorded for whatever comes next.
	if got := rows["record"]; !strings.Contains(got.Value, "recorded for") {
		t.Errorf("record = %q, want this tree's measurement recorded", got.Value)
	}

	// The second run hits the cache. Without this the assertion above passes on
	// an implementation that measures the base tree on every single run, which
	// is the cost the caching exists to pay once.
	out2, errOut2, err2 := runTestCmdStreams(t, root, "--gate-coverage", "--json")
	if err2 == nil {
		t.Fatalf("the gate passed on the second run\nstdout: %s\nstderr: %s", out2, errOut2)
	}
	if got, ok := jsonRows(t, out2)["baseline"]; ok && strings.Contains(got.Value, "measuring it now") {
		t.Errorf("baseline = %q on the second run, want a cache hit", got.Value)
	}
}

// A change that raises coverage passes, and the run that follows it gates
// against the number it recorded. Without this the suite would be satisfied by
// a gate that fails everything.
func TestTheGatePassesAChangeThatRaisesCoverage(t *testing.T) {
	root := gateRepo(t)
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), root, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run("switch", "--quiet", "-c", "better")
	write(t, root, "svc/lib_test.go", "package svc\n\nimport \"testing\"\n\nfunc TestKeep(t *testing.T) {\n\tif Keep(1) != 2 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n\nfunc TestSkip(t *testing.T) {\n\tif Skip(2) != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	run("add", "-A")
	run("commit", "-m", "test the other branch")

	out, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json")
	if err != nil {
		t.Fatalf("the gate failed a change that raised coverage: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	rows := jsonRows(t, out)
	if got := rows["coverage(svc)"]; got.Status != "pass" {
		t.Errorf("coverage(svc) = %+v, want a pass", got)
	}
	if got := rows["coverage"]; got.Status != "pass" {
		t.Errorf("coverage = %+v, want a pass", got)
	}
}

// jsonRows decodes the document's rows by label.
func jsonRows(t *testing.T, out string) map[string]struct {
	Status string   `json:"status"`
	Label  string   `json:"label"`
	Value  string   `json:"value"`
	Detail []string `json:"detail"`
} {
	t.Helper()
	var doc struct {
		Rows []struct {
			Status string   `json:"status"`
			Label  string   `json:"label"`
			Value  string   `json:"value"`
			Detail []string `json:"detail"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not a JSON document (%v):\n%s", err, out)
	}
	byLabel := map[string]struct {
		Status string   `json:"status"`
		Label  string   `json:"label"`
		Value  string   `json:"value"`
		Detail []string `json:"detail"`
	}{}
	for _, r := range doc.Rows {
		byLabel[r.Label] = r
	}
	return byLabel
}
