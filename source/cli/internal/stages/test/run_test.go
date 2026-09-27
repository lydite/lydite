package teststages

import (
	"bytes"
	"context"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// refusingLogs fails the test if any log is opened: a run that starts nothing
// has nothing to write.
func refusingLogs(t *testing.T) Logs {
	return func(string, string, bool) (testrun.Opener, func()) {
		t.Error("a log was opened for a run that starts nothing")
		return func(component.Component, int, bool) testrun.Output { return testrun.Output{} }, func() {}
	}
}

// A run that selected nothing runs nothing and reports no schedule row: the
// skipped rows are the whole set, in the order the run owns them.
func TestRunWithNothingSelectedReportsTheSkippedRowsAlone(t *testing.T) {
	own := []component.Component{
		{Name: "a", Dir: "a", Runner: runner.GoTest},
		{Name: "b", Dir: "b", Runner: runner.GoTest},
		{Name: "c", Dir: "c", Runner: runner.GoTest},
	}
	skipped := map[string]ui.Row{
		"c": {Status: ui.StatusUnmeasured, Label: "test(c)", Value: "not affected"},
		"a": {Status: ui.StatusUnmeasured, Label: "test(a)", Value: "not affected"},
	}
	gate := testrun.NewFlakyGate(context.Background(), t.TempDir(), "", false)

	out, err := Run(context.Background(), RunIn{
		Dir: t.TempDir(), Decl: component.File{Components: own}, Own: own, Ordered: own, Skipped: skipped,
		Config: config.Default(), Concurrency: 1, Gate: gate, Logs: refusingLogs(t),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := labels(out.Rows); got != "test(a),test(c)" {
		t.Errorf("rows = %s, want the skipped rows in declaration order and no schedule row", got)
	}
	if out.Measurements != nil {
		t.Errorf("measurements = %+v, want none from a run that ran nothing", out.Measurements)
	}
	if out.Gate != gate {
		t.Error("the flaky gate was not handed on")
	}
}

// A declaration holding no component says so through the report, and runs
// nothing.
func TestRunOverNoDeclarationSaysSo(t *testing.T) {
	out, err := Run(context.Background(), RunIn{Dir: t.TempDir(), Config: config.Default(), Logs: refusingLogs(t)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Label != "test" || out.Rows[0].Status != ui.StatusUnmeasured ||
		out.Rows[0].Value != "no components declared in "+component.FileName {
		t.Errorf("rows = %+v, want the one row saying nothing is declared", out.Rows)
	}
}

// Each component writes to the log Logs opened for it under the scan root,
// the report names that log, and every log is closed once the run is over.
func TestRunWritesThroughTheLogsItIsGivenAndClosesThem(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mod/.keep", "")
	c := component.Component{Name: "a", Dir: "mod", Command: []string{"sh", "-c", "echo from the suite"}}
	var got bytes.Buffer
	var openedUnder, openedAs string
	closed := 0
	logs := func(r, k string, stream bool) (testrun.Opener, func()) {
		openedUnder, openedAs = r, k
		if !stream {
			t.Error("the run asked for no mirror to the terminal, and was given none")
		}
		return func(component.Component, int, bool) testrun.Output {
				return testrun.Output{W: &got, Rel: "reports/a/test.log"}
			}, func() {
				if got.Len() == 0 {
					t.Error("the logs were closed before the suite wrote to them")
				}
				closed++
			}
	}

	out, err := Run(context.Background(), RunIn{
		Dir: root, Decl: component.File{Components: []component.Component{c}},
		Own: []component.Component{c}, Selected: []component.Component{c},
		Config: config.Default(), Concurrency: 1, Stream: true, Logs: logs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if openedUnder != root || openedAs != "test" {
		t.Errorf("logs opened under %q as %q, want %q as test", openedUnder, openedAs, root)
	}
	if closed != 1 {
		t.Errorf("logs closed %d time(s), want once", closed)
	}
	if got.String() != "from the suite\n" {
		t.Errorf("log = %q, want the suite's output", got.String())
	}
	if labels(out.Rows) != "schedule,test(a)" || out.Rows[1].Status != ui.StatusPass || out.Rows[1].Log != "reports/a/test.log" {
		t.Errorf("rows = %+v, want the schedule row and a passing test(a) naming its log", out.Rows)
	}
	if len(out.Measurements) != 1 || out.Measurements[0].Name != "a" {
		t.Errorf("measurements = %+v, want one for a", out.Measurements)
	}
}
