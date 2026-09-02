package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
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
	if err := addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl, ms, config.Default(),
		coverageOptions{Instrument: true}); err != nil {
		t.Fatal(err)
	}
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
	if err := addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl,
		[]measurement{unmeasuredComponent(decl.Components[0], "coverage is off for this run")},
		config.Default(), coverageOptions{}); err != nil {
		t.Fatal(err)
	}
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
	got := inDeclarationOrder(decl, []measurement{measured("api", runner.Go, 1, 2)})
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
		"api/go.mod":                       "module example.com/api\n\ngo 1.26\n",
		"api/main.go":                      "package api\n\nfunc F() int {\n\treturn 1\n}\n",
		"api/.lydite-reports/coverage.out": "mode: set\nexample.com/api/main.go:3.16,5.2 1 1\n",
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
	ms := inDeclarationOrder(decl, nil)
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
	if err := addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl, ms, config.Default(),
		coverageOptions{Instrument: true}); err != nil {
		t.Fatal(err)
	}
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
	if err := addCoverageRows(context.Background(), newTestCmd(), rep, t.TempDir(), decl, ms, config.Default(),
		coverageOptions{Instrument: true}); err != nil {
		t.Fatal(err)
	}
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
	report := ".lydite-reports/coverage.out"
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
	if err := addCoverageRows(context.Background(), cmd, rep, root, decl, ms, config.Default(),
		coverageOptions{Instrument: true, Gate: true, Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
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
