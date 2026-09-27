package teststages

import (
	"context"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	testmeasure "lydite/lydite/internal/test/measure"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// RunIn is everything the scheduled run needs.
type RunIn struct {
	Dir  string
	Decl component.File
	// Own is the responsibility set, which the skipped rows are reported over
	// when nothing was selected.
	Own []component.Component
	// Selected, Ordered and Skipped are SelectAffected's.
	Selected []component.Component
	Ordered  []component.Component
	Skipped  map[string]ui.Row
	Config   config.Config
	Envs     toolchain.Envs
	// Concurrency is how many components run at once.
	Concurrency int
	// Stream mirrors each component's output to the terminal as well as to
	// its log.
	Stream bool
	// Instrument runs each component's instrumented variant rather than its
	// plain one.
	Instrument bool
	// Gate is PrepareFlakyGate's, which each component's own run carries out.
	Gate *testrun.FlakyGate
	// Logs opens each component's log.
	Logs Logs
}

// RunOut is the run's section of the report, and what it measured.
type RunOut struct {
	// Rows is the schedule row and one test(<name>) row per component in
	// Own, in declaration order — or, when nothing was selected, only the
	// rows selection skipped.
	Rows []ui.Row
	// Measurements is one per selected component, nil when nothing ran.
	Measurements []testmeasure.Measurement
	// Gate is In's Gate, holding what each component's run established about
	// the tests it introduced, for FlakyGate to report.
	Gate *testrun.FlakyGate
}

// Run plans every selected component and runs them under the scheduler.
//
// A run that selected nothing runs nothing, and reports no schedule row: the
// skipped rows are the whole set, still in declaration order. A declaration
// holding no component at all says so, through the report rather than around
// it — under --json stdout carries a document and nothing else, and a bare
// sentence would be unparseable output. With nothing declared there is nothing
// in Own, so FlakyGate and Coverage report nothing after this row and it is the
// last row of the report.
func Run(ctx context.Context, in RunIn) (RunOut, error) {
	out := RunOut{Gate: in.Gate}
	if len(in.Selected) == 0 {
		for _, c := range in.Own {
			if r, ok := in.Skipped[c.Name]; ok {
				out.Rows = append(out.Rows, r)
			}
		}
		if len(in.Decl.Components) == 0 {
			out.Rows = append(out.Rows, ui.Row{
				Status: ui.StatusUnmeasured,
				Label:  kind,
				Value:  "no components declared in " + component.FileName,
			})
		}
		return out, nil
	}
	out.Rows, out.Measurements = runComponents(ctx, in.Dir, in.Selected, in.Ordered, in.Skipped,
		in.Config, in.Envs, in.Concurrency, in.Stream, in.Instrument, in.Logs, in.Gate)
	return out, nil
}

// runComponents plans selected under root through logs, runs them with
// testrun.RunComponents, and closes every log once the run is over. It is the
// one path a suite runs by: this run's, and a base tree measured in a
// throwaway worktree, which is only a baseline worth comparing against when it
// was measured exactly as the tree it is compared with.
func runComponents(ctx context.Context, root string, selected, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, limit int, stream, instrument bool, logs Logs, gate *testrun.FlakyGate) ([]ui.Row, []testmeasure.Measurement) {
	open, closeAll := logs.open(root, stream)
	defer closeAll()
	plans := testrun.PlanComponents(ctx, root, selected, kind, open)
	return testrun.RunComponents(ctx, root, plans, ordered, skipped, cfg, envs, limit, instrument, gate)
}
