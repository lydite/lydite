package teststages

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// A gate nobody asked for is still built, and still reports: a row per
// component saying it was not gated, because a section that disappears reads
// as a concern that passed.
func TestPrepareFlakyGateBuildsAGateNobodyAskedFor(t *testing.T) {
	out, err := PrepareFlakyGate(context.Background(), PrepareFlakyGateIn{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("PrepareFlakyGate: %v", err)
	}
	report, err := FlakyGate(context.Background(), FlakyGateIn{
		Gate: out.Gate,
		Own:  []component.Component{{Name: "api", Dir: "api", Runner: runner.GoTest}},
	})
	if err != nil {
		t.Fatalf("FlakyGate: %v", err)
	}
	if len(report.Rows) != 1 || report.Rows[0].Status != ui.StatusContext || !strings.Contains(report.Rows[0].Value, "not gated") {
		t.Errorf("rows = %+v, want one context row saying nothing was gated", report.Rows)
	}
}

// Every run builds a gate, so a report with none handed to it is a wiring
// fault, said as one rather than a panic.
func TestFlakyGateWithNoGateIsAnError(t *testing.T) {
	if _, err := FlakyGate(context.Background(), FlakyGateIn{}); !errors.Is(err, errNoGate) {
		t.Errorf("err = %v, want errNoGate", err)
	}
}

// The gate examines each component from inside that component's own run, and
// reports afterwards. Run and FlakyGate are two stages over one gate: what the
// examination found during Run has to reach FlakyGate unchanged, and Run's
// rows followed by FlakyGate's have to be exactly what one pass running and
// reporting both produces — the invariant ADR 0062 records.
//
// a declares a raw command, so the gate records its verdict during a's run;
// b declares no suite, so its row comes from the declaration; c is skipped by
// selection, so the gate never examined it. A gate whose state were lost
// between the stages would report a as never having run.
func TestRunThenFlakyGateReportWhatOnePassRunningBothReports(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mod/.keep", "")
	own := []component.Component{
		{Name: "a", Dir: "mod", Command: []string{"sh", "-c", "exit 0"}},
		{Name: "b", Dir: "scripts", DeclaredLang: runner.Shell},
		{Name: "c", Dir: "mod", Runner: runner.GoTest},
	}
	selected := own[:2]
	skipped := map[string]ui.Row{"c": {Status: ui.StatusUnmeasured, Label: "test(c)", Value: "not affected"}}
	cfg := config.Default()

	// One pass: the run carries the gate out, and the gate reports straight
	// after it.
	fused := testrun.NewFlakyGate(context.Background(), root, "", true)
	plans := testrun.PlanComponents(context.Background(), root, selected, "test", func(component.Component, int, bool) testrun.Output {
		return testrun.Output{W: io.Discard}
	})
	wantRows, wantMeasured := testrun.RunComponents(context.Background(), root, plans, own, skipped, cfg, nil, 1, false, fused)
	flakyRows, wantFound := fused.Report(own)
	wantRows = append(wantRows, flakyRows...)

	// The stages.
	prepared, err := PrepareFlakyGate(context.Background(), PrepareFlakyGateIn{Dir: root, Requested: true})
	if err != nil {
		t.Fatalf("PrepareFlakyGate: %v", err)
	}
	ran, err := Run(context.Background(), RunIn{
		Dir: root, Decl: component.File{Components: own}, Own: own,
		Selected: selected, Ordered: own, Skipped: skipped,
		Config: cfg, Concurrency: 1, Gate: prepared.Gate,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran.Gate != prepared.Gate {
		t.Fatal("Run handed on a gate other than the one it ran with")
	}
	reported, err := FlakyGate(context.Background(), FlakyGateIn{Gate: ran.Gate, Own: own})
	if err != nil {
		t.Fatalf("FlakyGate: %v", err)
	}
	gotRows := append(append([]ui.Row(nil), ran.Rows...), reported.Rows...)

	if !reflect.DeepEqual(gotRows, wantRows) {
		t.Errorf("rows =\n%+v\nwant\n%+v", gotRows, wantRows)
	}
	if !reflect.DeepEqual(reported.Findings, wantFound) {
		t.Errorf("findings = %+v, want %+v", reported.Findings, wantFound)
	}
	if !reflect.DeepEqual(ran.Measurements, wantMeasured) {
		t.Errorf("measurements = %+v, want %+v", ran.Measurements, wantMeasured)
	}
	if got := labels(gotRows); got != "schedule,test(a),test(b),test(c),flaky(a),flaky(b),flaky(c)" {
		t.Errorf("rows = %s, want the run's section and then the gate's", got)
	}
	if a := row(t, reported.Rows, "flaky(a)"); !strings.Contains(a.Value, "raw command") {
		t.Errorf("flaky(a) = %+v, want the verdict a's own run recorded", a)
	}
	if c := row(t, reported.Rows, "flaky(c)"); !strings.Contains(c.Value, "did not run") {
		t.Errorf("flaky(c) = %+v, want the component selection skipped named as unexamined", c)
	}
}
