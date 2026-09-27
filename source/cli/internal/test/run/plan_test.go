package run

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// The opener is asked once per selected component, in selection order, with
// the width every prefix aligns to and whether anything will ever write to the
// output — a component declaring no suite runs nothing, so a caller opening a
// file for it would leave an empty log reading as a run that never happened.
func TestPlanComponentsAsksTheOpenerOncePerComponent(t *testing.T) {
	type call struct {
		name  string
		width int
		runs  bool
	}
	var calls []call
	selected := []component.Component{
		{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
		{Name: "api", Dir: "api", Runner: runner.GoTest},
	}
	plans := PlanComponents(context.Background(), t.TempDir(), selected, "test", func(c component.Component, width int, runs bool) Output {
		calls = append(calls, call{c.Name, width, runs})
		return Output{W: io.Discard, Rel: c.Name + ".log"}
	})

	want := []call{{"scripts", 7, false}, {"api", 7, true}}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
	if len(plans) != 2 || plans[0].C.Name != "scripts" || plans[1].C.Name != "api" {
		t.Fatalf("plans = %+v, want one per component in selection order", plans)
	}
	if plans[0].Ready || plans[0].Row.Label != "test(scripts)" || !strings.Contains(plans[0].Row.Value, NoSuiteReason) {
		t.Errorf("scripts = %+v, want unrunnable with its declaration's row", plans[0])
	}
	if !plans[1].Ready || plans[1].Out.Rel != "api.log" {
		t.Errorf("api = %+v, want runnable, writing where the opener said", plans[1])
	}
}

// A compose file that will not load fails its component at planning, and the
// row names the log the opener gave it: the tail is empty because nothing ran,
// so the path is the only pointer a reader has.
func TestAPlanningFailureNamesTheOpenedLog(t *testing.T) {
	root := goModule(t)
	write(t, root, "mod/compose.yaml", "services:\n  db:\n    image: postgres\n")
	c := component.Component{
		Name: "broken", Dir: "mod", Runner: runner.GoTest,
		Compose: component.Compose{File: "./compose.yaml", Up: []string{"ghost"}},
	}
	plans := PlanComponents(context.Background(), root, []component.Component{c}, "test", func(component.Component, int, bool) Output {
		return Output{W: io.Discard, Rel: "reports/broken/test.log"}
	})
	row := plans[0].Row
	if plans[0].Ready || row.Status != ui.StatusFail || row.Value != "services not started" {
		t.Fatalf("plan = %+v, want an unrunnable component failing on its services", plans[0])
	}
	if row.Log != "reports/broken/test.log" || !strings.Contains(strings.Join(row.Detail, "\n"), "full output: reports/broken/test.log") {
		t.Errorf("row = %+v, want the opened log named", row)
	}
}

// The rows come back in the order a report shows them: the schedule row, then
// each component in declaration order with selection's skipped rows where the
// declaration puts them — never in the order the scheduler finished them.
func TestRunComponentsReturnsRowsInDeclarationOrder(t *testing.T) {
	root := goModule(t)
	declared := []component.Component{
		{Name: "c", Dir: "mod", Command: []string{"sh", "-c", "exit 0"}},
		{Name: "skipped", Dir: "mod", Runner: runner.GoTest},
		{Name: "a", Dir: "mod", Command: []string{"sh", "-c", "exit 0"}},
	}
	selected := []component.Component{declared[0], declared[2]}
	plans := PlanComponents(context.Background(), root, selected, "test", func(component.Component, int, bool) Output {
		return Output{W: io.Discard}
	})
	skipped := map[string]ui.Row{"skipped": {Status: ui.StatusUnmeasured, Label: TestLabel("skipped"), Value: "not affected"}}

	rows, ms := RunComponents(context.Background(), root, plans, declared, skipped, config.Default(), nil, 2, false, nil)

	var labels []string
	for _, r := range rows {
		labels = append(labels, r.Label)
	}
	want := []string{"schedule", "test(c)", "test(skipped)", "test(a)"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", labels, want)
	}
	if len(ms) != len(plans) || ms[0].Name != "c" || ms[1].Name != "a" {
		t.Errorf("measurements = %+v, want one per plan, in plan order", ms)
	}
}

// An exclude covering no file is a warning on the writer the caller named, and
// never a row: it leaves the gate stricter than declared, not weaker.
func TestOrphanRowWarnsAboutAnUnusedExcludeOnTheWriterItIsGiven(t *testing.T) {
	root := gitRepo(t, map[string]string{"cli/main.go": "package main\n"})
	file := component.File{
		Components: []component.Component{{Name: "cli", Dir: "cli", Runner: runner.GoTest}},
		Excludes:   []string{"nothing/**"},
	}
	var warned bytes.Buffer
	row := OrphanRow(context.Background(), root, file, &warned)
	if row.Status != ui.StatusPass {
		t.Errorf("row = %+v, want a pass: every source file is under a component", row)
	}
	if !strings.Contains(warned.String(), `exclude "nothing/**" covers no file`) {
		t.Errorf("warnings = %q, want the unused exclude named", warned.String())
	}
}
