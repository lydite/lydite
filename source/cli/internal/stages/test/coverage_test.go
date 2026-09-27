package teststages

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	testmeasure "lydite/lydite/internal/test/measure"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// measured is a component that produced a measurement.
func measured(name string, covered, total int) testmeasure.Measurement {
	return testmeasure.Measurement{Name: name, Dir: name, Lang: runner.Go, Lines: coverage.LineCount{Covered: covered, Total: total}}
}

// suitesAndOneWithout is two Go suites and a component declaring none.
func suitesAndOneWithout() component.File {
	return component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
		{Name: "sdk", Dir: "sdk", Runner: runner.GoTest},
		{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
	}}
}

// --no-coverage reports no coverage row at all, not even for a component
// declaring no suite: a row saying `unmeasured` on every fast local run trains
// readers to ignore the tag that exists to be noticed.
func TestCoverageWithoutInstrumentationReportsNothing(t *testing.T) {
	decl := suitesAndOneWithout()
	out, err := Coverage(context.Background(), CoverageIn{
		Dir: t.TempDir(), Decl: decl, Own: decl.Components, Config: config.Default(),
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if len(out.Rows) != 0 || len(out.NoSuiteRows) != 0 || out.Candidate != nil {
		t.Errorf("out = %+v, want nothing", out)
	}
}

// A run that measured and did not gate never renders as a pass, and says the
// baseline was not read. A component declaring no suite takes no part in any
// figure, and its own rows follow the section. A declaration covering no
// function is named on stderr.
func TestCoverageUngatedNeverRendersAsAPass(t *testing.T) {
	decl := suitesAndOneWithout()
	api := measured("api", 9, 10)
	api.Unused = []string{"api/x.go:3"}
	var warned strings.Builder

	out, err := Coverage(context.Background(), CoverageIn{
		Dir: t.TempDir(), Decl: decl, Own: decl.Components, Config: config.Default(),
		Measurements: []testmeasure.Measurement{api, measured("sdk", 8, 10)},
		Instrument:   true, Stderr: &warned,
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if got := labels(out.Rows); !strings.HasPrefix(got, "coverage(api),crap(api),coverage(sdk),crap(sdk),coverage(repo),") ||
		strings.Contains(got, "scripts") {
		t.Errorf("rows = %s, want each suite's coverage beside its CRAP, then the repository, and nothing about scripts", got)
	}
	for _, label := range []string{"coverage(api)", "coverage(sdk)", "coverage(repo)"} {
		if r := row(t, out.Rows, label); r.Status != ui.StatusContext {
			t.Errorf("%s = %+v, want context: nothing was gated", label, r)
		}
	}
	if b := row(t, out.Rows, "baseline"); b.Status != ui.StatusContext || !strings.Contains(b.Value, "pass --gate-coverage") {
		t.Errorf("baseline = %+v, want context naming the flag", b)
	}
	want := testrun.NoSuiteCoverageRows("scripts", 0)
	if labels(out.NoSuiteRows) != labels(want) {
		t.Errorf("no-suite rows = %s, want %s", labels(out.NoSuiteRows), labels(want))
	}
	if out.Candidate != nil || out.Findings != nil {
		t.Errorf("candidate = %+v, findings = %+v, want neither from a run that compared nothing", out.Candidate, out.Findings)
	}
	if !strings.Contains(warned.String(), "warning: api/x.go:3: ") {
		t.Errorf("stderr = %q, want the declaration covering no function named", warned.String())
	}
}

// A component this run was responsible for and never measured still takes its
// rows, and a run narrowed by `--component` answers nothing about the
// repository.
func TestCoverageNarrowedAccountsForEveryOwnedComponentAndNotTheRepository(t *testing.T) {
	decl := suitesAndOneWithout()
	out, err := Coverage(context.Background(), CoverageIn{
		Dir: t.TempDir(), Decl: decl, Own: decl.Components[:2], Config: config.Default(),
		Measurements: []testmeasure.Measurement{measured("api", 9, 10)},
		Instrument:   true, Narrowed: true,
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if strings.Contains(labels(out.Rows), "(repo)") {
		t.Errorf("rows = %s, want no figure over the repository from a narrowed run", labels(out.Rows))
	}
	if r := row(t, out.Rows, "coverage(sdk)"); r.Status != ui.StatusUnmeasured {
		t.Errorf("coverage(sdk) = %+v, want unmeasured: this run owned it and did not measure it", r)
	}
}

// A gate asked for and not run fails through a row and keeps the
// measurements, because they are what a reader needs to act on it.
func TestCoverageThatCouldNotGateFailsThroughARow(t *testing.T) {
	decl := suitesAndOneWithout()
	out, err := Coverage(context.Background(), CoverageIn{
		Dir: t.TempDir(), Decl: decl, Own: decl.Components[:1], Config: config.Default(),
		Measurements: []testmeasure.Measurement{measured("api", 9, 10)},
		Instrument:   true, Gate: true, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if b := row(t, out.Rows, "baseline"); b.Status != ui.StatusFail || b.Value != "not gated" || len(b.Detail) == 0 {
		t.Errorf("baseline = %+v, want a failure with its reason", b)
	}
	row(t, out.Rows, "coverage(api)")
	if out.Candidate != nil {
		t.Errorf("candidate = %+v, want none from a gate that could not run", out.Candidate)
	}
}

// On the default branch HEAD is its own merge-base: nothing is compared, the
// figures render as an ungated run's do, and this tree's measurement is
// proposed for recording.
func TestCoverageOnItsOwnMergeBaseProposesThisTree(t *testing.T) {
	root := pushedRepo(t, map[string]string{"api/x.go": "package api\n"})
	decl := component.File{Components: []component.Component{{Name: "api", Dir: "api", Runner: runner.GoTest}}}

	out, err := Coverage(context.Background(), CoverageIn{
		Dir: root, Decl: decl, Own: decl.Components, Config: config.Default(),
		Measurements: []testmeasure.Measurement{measured("api", 9, 10)},
		Instrument:   true, Gate: true, Concurrency: 1,
		Logs: refusingLogs(t),
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	last := out.Rows[len(out.Rows)-1]
	if last.Label != "baseline" || last.Status != ui.StatusContext || !strings.Contains(last.Value, "HEAD is its own merge-base") {
		t.Errorf("last row = %+v, want the baseline row saying nothing was compared", last)
	}
	if r := row(t, out.Rows, "coverage(api)"); r.Status != ui.StatusContext {
		t.Errorf("coverage(api) = %+v, want context: nothing was compared", r)
	}
	tree, err := gitstate.TreeSHA(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	c := out.Candidate
	if c == nil || c.Reason != "" || c.Tree != tree || c.Gated || c.GatedAgainst != nil {
		t.Fatalf("candidate = %+v, want this tree's, ungated", c)
	}
	if e, ok := c.Record["api"]; !ok || e.LineCount != (coverage.LineCount{Covered: 9, Total: 10}) {
		t.Errorf("record = %+v, want api's measurement", c.Record)
	}
	if want := "1 of 1 component(s) ready for " + shortSHA(tree) + " — `lydite test record` lands it"; c.Value != want {
		t.Errorf("value = %q, want %q", c.Value, want)
	}
}

// A component this run selected and could not measure blocks the recording:
// the candidate still carries what was measured, and the record row says what
// blocked it.
func TestCoverageNamesTheComponentThatBlocksItsRecording(t *testing.T) {
	root := pushedRepo(t, map[string]string{"api/x.go": "package api\n"})
	decl := suitesAndOneWithout()
	var warned strings.Builder

	out, err := Coverage(context.Background(), CoverageIn{
		Dir: root, Decl: decl, Own: decl.Components, Config: config.Default(),
		Measurements: []testmeasure.Measurement{
			measured("api", 9, 10),
			testmeasure.UnmeasuredComponent(decl.Components[1], "test(sdk) did not pass: failed"),
		},
		Instrument: true, Gate: true, Concurrency: 1, Stderr: &warned,
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	c := out.Candidate
	if c == nil || c.Reason != "" || len(c.Record) != 1 {
		t.Fatalf("candidate = %+v, want api's measurement kept", c)
	}
	if !strings.HasPrefix(c.Value, "not recorded — sdk has no coverage to record") {
		t.Errorf("value = %q, want sdk named as what blocks the recording", c.Value)
	}
	if !strings.Contains(warned.String(), `component "sdk" has no coverage to record`) {
		t.Errorf("stderr = %q, want the blocked recording explained", warned.String())
	}
}

// A base tree with no cached baseline is measured in a throwaway worktree,
// through the logs the run was given, at the base tree's own scan root. A
// base tree that measured nothing is said so, and a run that measured nothing
// proposes a candidate that establishes nothing.
func TestCoverageMeasuresAnUncachedBaseTreeInAWorktree(t *testing.T) {
	declared := "components:\n  - name: sh\n    dir: .\n    command: [\"sh\", \"-c\", \"exit 0\"]\n"
	root := pushedRepo(t, map[string]string{component.FileName: declared})
	commit(t, root, "README.md", "a change\n")
	decl := component.File{Components: []component.Component{{Name: "sh", Dir: ".", Command: []string{"sh", "-c", "exit 0"}}}}
	var roots []string
	logs := func(r, _ string, stream bool) (testrun.Opener, func()) {
		roots = append(roots, r)
		if stream {
			t.Error("a base tree's run was mirrored to the terminal")
		}
		return Logs(nil).open(r, stream)
	}
	var warned strings.Builder

	out, err := Coverage(context.Background(), CoverageIn{
		Dir: root, Decl: decl, Own: decl.Components, Config: config.Default(),
		Measurements: []testmeasure.Measurement{testmeasure.UnmeasurableComponent(decl.Components[0], "a raw command")},
		Instrument:   true, Gate: true, Concurrency: 1, Logs: logs, Stderr: &warned,
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if len(roots) != 1 || roots[0] == root || !strings.HasPrefix(filepath.Base(roots[0]), "lydite-baseline-") {
		t.Errorf("logs opened under %v, want once, under a throwaway worktree", roots)
	}
	if first := out.Rows[0]; first.Label != "baseline" || !strings.Contains(first.Value, "measuring it now") {
		t.Errorf("first row = %+v, want the base tree's measurement announced", first)
	}
	if !strings.Contains(warned.String(), "measured no coverage at all at ") {
		t.Errorf("stderr = %q, want a base tree that measured nothing named", warned.String())
	}
	if c := out.Candidate; c == nil || c.Reason != "no component produced a measurement" || c.Tree == "" || !strings.HasPrefix(c.Value, "nothing to record — ") {
		t.Errorf("candidate = %+v, want one that establishes nothing and names its tree", c)
	}
}
