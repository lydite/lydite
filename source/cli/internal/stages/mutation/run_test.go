package mutationstages

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/toolchain"
)

// rowError stands in for the error a command's Lifecycle returns for a row it
// has already decided: its text is the row's detail, joined.
type rowError struct{ detail []string }

func (e rowError) Error() string { return strings.Join(e.detail, "; ") }

// lockedBuffer is a log several goroutines may write at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// webSource is one TypeScript function whose second line holds a comparison
// the catalogue mutates.
const webSource = "export function less(x: number, y: number): boolean {\n  return x < y;\n}\n"

// lcovExecuting is the baseline that reports the comparison's line executed,
// and lcovNotExecuting the one that reports it listed and never run.
const (
	lcovExecuting    = `mkdir -p coverage && printf 'SF:src/a.ts\nDA:2,1\nLF:1\nLH:1\nend_of_record\n' > coverage/lcov.info`
	lcovNotExecuting = `mkdir -p coverage && printf 'SF:src/a.ts\nDA:2,0\nLF:1\nLH:0\nend_of_record\n' > coverage/lcov.info`
)

// webFixture is a TypeScript component whose mutants run in a worker
// directory, and whose every command is a shell one-liner the test chooses —
// so each way a component can end is reachable without a Node toolchain.
type webFixture struct {
	root    string
	c       component.Component
	changed map[string][]int
	files   []string
	log     *lockedBuffer
	calls   *calls
}

func newWebFixture(t *testing.T) *webFixture {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "web/src/a.ts", webSource)
	return &webFixture{
		root:    root,
		c:       component.Component{Name: "web", Dir: "web", Runner: "vitest"},
		changed: map[string][]int{"web/src/a.ts": {2}},
		files:   []string{"web/src/a.ts"},
		log:     &lockedBuffer{},
		calls:   &calls{},
	}
}

// shape answers for the fixture's component with the three scripts given, one
// per variant.
func (f *webFixture) shape(t *testing.T, baseline, build, suite string) *fakeShape {
	return &fakeShape{
		t: t,
		invocation: func(_ component.Component, v runner.Variant) (runner.Invocation, error) {
			switch v {
			case runner.Instrumented:
				return runner.Invocation{Name: "sh", Args: []string{"-c", baseline}, CoverageReport: "coverage/lcov.info"}, nil
			case runner.BuildOnly:
				return runner.Invocation{Name: "sh", Args: []string{"-c", build}}, nil
			default:
				return runner.Invocation{Name: "sh", Args: []string{"-c", suite}}, nil
			}
		},
		lang:    func(component.Component) runner.Lang { return runner.TypeScript },
		env:     func(*toolchain.Env, component.Component, runner.Invocation) []string { return nil },
		noSuite: func(component.Component) bool { return false },
		scope:   func(changed map[string][]int, _ component.Component) map[string][]int { return changed },
	}
}

// lifecycle plans every selected component ready, and records each call it
// answers, succeeding at every one.
func (f *webFixture) lifecycle(t *testing.T) *fakeLifecycle {
	return &fakeLifecycle{
		t: t,
		plan: func(_ context.Context, root string, selected []component.Component, _ bool) []Planned {
			f.calls.add("plan " + root)
			out := make([]Planned, len(selected))
			for i, c := range selected {
				out[i] = Planned{Component: c, Log: f.log, LogRel: ".lydite-reports/" + c.Name + "/mutation.log",
					Ready: true, Item: scheduler.Item{Name: c.Name, Dir: c.Dir}}
			}
			return out
		},
		close: func() { f.calls.add("close") },
		clearReport: func(dir, report string) error {
			f.calls.add("clear " + report)
			return nil
		},
		prepare: func(_ context.Context, name string, _ runner.Invocation, _, root string, _ config.Config, _ *toolchain.Env) error {
			f.calls.add("prepare " + name + " root=" + root)
			return nil
		},
		startServices: func(_ context.Context, name string) (func(), error) {
			f.calls.add("services " + name)
			return func() { f.calls.add("stop " + name) }, nil
		},
		runCommands: func(_ context.Context, name, _, kind string, _ []string, _ *toolchain.Env) error {
			f.calls.add(kind + " " + name)
			return nil
		},
	}
}

func (f *webFixture) in(shape Shape, lifecycle Lifecycle) RunMutantsIn {
	return RunMutantsIn{
		Shape:       shape,
		Lifecycle:   lifecycle,
		Dir:         f.root,
		Selected:    []component.Component{f.c},
		Changed:     f.changed,
		Files:       f.files,
		Limit:       1,
		Diagnostics: io.Discard,
	}
}

// only runs the fixture's one component and returns its outcome.
func only(t *testing.T, in RunMutantsIn) ComponentOutcome {
	t.Helper()
	out, err := RunMutants(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Components) != 1 {
		t.Fatalf("%d outcome(s), want one", len(out.Components))
	}
	if out.Interrupted {
		t.Error("a run nothing cancelled reports itself interrupted")
	}
	if out.DeadlineReached {
		t.Error("a run with no deadline reports reaching one")
	}
	return out.Components[0]
}

// A component whose mutants ran reports every one of them, the lines they
// came from and how long it took — and each step around its suite ran in the
// order a component's lifecycle needs: its report cleared, its runner
// prepared, its services up, its setup run, and its teardown and services down
// once the mutants are done.
func TestAComponentWhoseMutantsRanReportsWhatBecameOfThem(t *testing.T) {
	f := newWebFixture(t)
	o := only(t, f.in(f.shape(t, lcovExecuting, "true", "true"), f.lifecycle(t)))

	if o.Kind != KindCompleted || !o.Ran() {
		t.Fatalf("kind = %s (%v), want completed", o.Kind, o.Err)
	}
	if len(o.Results) == 0 || o.Summary.Survived != len(o.Results) {
		t.Errorf("summary = %+v over %d result(s), want every mutant surviving a suite that asserts nothing", o.Summary, len(o.Results))
	}
	if !reflect.DeepEqual(o.Scoped, f.changed) {
		t.Errorf("scoped = %v, want the changed lines the mutants came from", o.Scoped)
	}
	if o.Elapsed <= 0 {
		t.Error("a component whose mutants ran took no time")
	}
	if o.LogRel != ".lydite-reports/web/mutation.log" || o.Component.Name != "web" {
		t.Errorf("outcome names %q with log %q", o.Component.Name, o.LogRel)
	}
	if o.TeardownErr != nil {
		t.Errorf("teardown %v, want none", o.TeardownErr)
	}
	if !o.Scheduled {
		t.Error("a component whose mutants ran is not marked scheduled")
	}

	// The worker's preparation is passed no root: its dir is inside a copy of
	// the scan root, so the bound has to come from the copy's own tree. The
	// component's own is passed the real one.
	want := []string{
		"plan " + f.root,
		"clear coverage/lcov.info",
		"prepare web root=" + f.root,
		"services web",
		"setup web",
		"prepare web root=",
		"teardown web",
		"stop web",
		"close",
	}
	if got := f.calls.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if !strings.Contains(f.log.String(), "mutant(s), budget ") {
		t.Errorf("the log carries no projection:\n%s", f.log.String())
	}
}

// A caller's own Diagnostics writer is used exactly as given, never silently
// swapped out for another one: the defaulting to io.Discard only fills a nil
// Diagnostics, so a declaration that matched no mutant must still reach a
// writer the caller supplied.
func TestADiagnosticsWriterTheCallerGaveIsNeverReplaced(t *testing.T) {
	f := newWebFixture(t)
	writeFile(t, f.root, "web/src/a.ts", webSource+
		"// [lydite:exclude_from_mutation][nothing on this line is ever mutated]\n")
	var buf bytes.Buffer
	in := f.in(f.shape(t, lcovExecuting, "true", "true"), f.lifecycle(t))
	in.Diagnostics = &buf
	if _, err := RunMutants(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "covers no mutant") {
		t.Errorf("diagnostics = %q, want the unmatched declaration named on it", buf.String())
	}
}

// A declaration covering no mutant reaches the component's summary as a count,
// beside the line naming it on diagnostics, and gates nothing: the component's
// mutants run and are reported exactly as they would be without it.
func TestADeclarationCoveringNoMutantIsCountedInTheSummary(t *testing.T) {
	f := newWebFixture(t)
	writeFile(t, f.root, "web/src/a.ts", webSource+
		"// [lydite:exclude_from_mutation][nothing on this line is ever mutated]\n")
	var buf bytes.Buffer
	in := f.in(f.shape(t, lcovExecuting, "true", "exit 1"), f.lifecycle(t))
	in.Diagnostics = &buf
	o := only(t, in)
	if o.Kind != KindCompleted {
		t.Fatalf("kind = %s (%v), want completed", o.Kind, o.Err)
	}
	if o.Summary.Unmatched != 1 {
		t.Errorf("summary = %+v, want the one unmatched declaration counted", o.Summary)
	}
	if o.Summary.Killed == 0 || !o.Summary.Passed() {
		t.Errorf("summary = %+v, want every mutant killed and the gate unmoved by the count", o.Summary)
	}
	if strings.Count(buf.String(), "covers no mutant") != 1 {
		t.Errorf("diagnostics = %q, want the unmatched declaration named once", buf.String())
	}
}

// A suite that notices the change kills the mutant.
func TestASuiteThatFailsKillsTheMutant(t *testing.T) {
	f := newWebFixture(t)
	o := only(t, f.in(f.shape(t, lcovExecuting, "true", "exit 1"), f.lifecycle(t)))
	if o.Kind != KindCompleted {
		t.Fatalf("kind = %s (%v), want completed", o.Kind, o.Err)
	}
	if o.Summary.Killed == 0 || o.Summary.Survived != 0 {
		t.Errorf("summary = %+v, want every mutant killed", o.Summary)
	}
}

// The worker's preparation failing is the Lifecycle's own account of it, and
// it reaches the executor's error exactly as a failing preparation always has:
// the worker names what it was doing, and the rest is the Lifecycle's text.
func TestAWorkerPreparationFailureReachesTheExecutorAsItsOwnText(t *testing.T) {
	f := newWebFixture(t)
	failed := rowError{detail: []string{"npm ci failed in web", "ERR! missing lockfile", "full output: .lydite-reports/web/mutation.log"}}
	lifecycle := f.lifecycle(t)
	inPlace := lifecycle.prepare
	lifecycle.prepare = func(ctx context.Context, name string, inv runner.Invocation, dir, root string, cfg config.Config, tc *toolchain.Env) error {
		if root == "" {
			return failed
		}
		return inPlace(ctx, name, inv, dir, root, cfg, tc)
	}
	o := only(t, f.in(f.shape(t, lcovExecuting, "true", "true"), lifecycle))

	if o.Kind != KindExecuteFailed {
		t.Fatalf("kind = %s (%v), want execute-failed", o.Kind, o.Err)
	}
	joined := strings.Join(failed.detail, "; ")
	if want := "preparing the worker directory: " + joined; o.Err.Error() != want {
		t.Errorf("the executor's error is %q, want %q", o.Err, want)
	}

	// The same text a preparation that built its own error out of the joined
	// detail reaches the executor with.
	own := mutation.Tree{Root: f.root, Component: "web", Files: f.files,
		Prepare: func(context.Context, string) error { return errors.New(joined) }}
	_, err := mutation.Execute(t.Context(), own, []mutation.Mutant{{Path: "src/a.ts", Line: 2}}, mutation.Options{Workers: 1})
	if err == nil || err.Error() != o.Err.Error() {
		t.Errorf("a preparation joining its own detail reached the executor as %v, want %q", err, o.Err)
	}
}

// Every way a component stops once it has been planned is its own outcome,
// carrying what its account needs and nothing it does not.
func TestEveryWayAComponentStopsIsItsOwnOutcome(t *testing.T) {
	refused := errors.New("refused")
	for _, c := range []struct {
		name     string
		baseline string
		memory   int64
		fixture  func(t *testing.T, f *webFixture)
		edit     func(*fakeLifecycle)
		kind     OutcomeKind
		err      error
		// want are the calls made, after planning and before closing.
		want  []string
		check func(t *testing.T, o ComponentOutcome)
	}{
		{
			name: "a report that could not be cleared", baseline: lcovExecuting,
			edit: func(l *fakeLifecycle) { l.clearReport = func(string, string) error { return refused } },
			kind: KindClearReportFailed, err: refused,
		},
		{
			name: "a component that could not be prepared", baseline: lcovExecuting,
			edit: func(l *fakeLifecycle) {
				l.prepare = func(context.Context, string, runner.Invocation, string, string, config.Config, *toolchain.Env) error {
					return refused
				}
			},
			kind: KindBlocked, err: refused, want: []string{"clear coverage/lcov.info"},
		},
		{
			name: "services that would not start", baseline: lcovExecuting,
			edit: func(l *fakeLifecycle) {
				l.startServices = func(context.Context, string) (func(), error) { return nil, refused }
			},
			kind: KindBlocked, err: refused,
		},
		{
			name: "a setup command that failed", baseline: lcovExecuting,
			edit: func(l *fakeLifecycle) {
				teardown := l.runCommands
				l.runCommands = func(ctx context.Context, name, dir, kind string, cmds []string, tc *toolchain.Env) error {
					if kind == "setup" {
						return refused
					}
					return teardown(ctx, name, dir, kind, cmds, tc)
				}
			},
			kind: KindBlocked, err: refused,
			want: []string{"clear coverage/lcov.info", "prepare web root=ROOT", "services web", "teardown web", "stop web"},
		},
		{
			name: "a baseline suite that did not pass", baseline: "echo the suite is red; exit 1",
			kind: KindBaselineFailed,
			want: []string{"clear coverage/lcov.info", "prepare web root=ROOT", "services web", "setup web", "teardown web", "stop web"},
			check: func(t *testing.T, o ComponentOutcome) {
				if !strings.Contains(o.Output, "the suite is red") {
					t.Errorf("output = %q, want what the baseline printed", o.Output)
				}
			},
		},
		{
			name: "a baseline that leaves no room under the bound", baseline: lcovExecuting, memory: 1,
			kind: KindBaselineTooLarge,
			check: func(t *testing.T, o ComponentOutcome) {
				if o.Peak <= 0 || o.Bound != 1 {
					t.Errorf("peak %d under bound %d, want the baseline's own peak under the override", o.Peak, o.Bound)
				}
			},
		},
		{
			name: "a coverage report that is not there", baseline: "true",
			kind: KindMeasureFailed,
			check: func(t *testing.T, o ComponentOutcome) {
				if o.Err == nil || !strings.Contains(o.Err.Error(), "coverage report") {
					t.Errorf("err = %v, want the report that could not be read", o.Err)
				}
			},
		},
		{
			name: "no changed line that ran", baseline: lcovNotExecuting,
			kind: KindNothingToMutate,
		},
		{
			// A directory where the changed file should be: the report reads
			// as executed, and the source it names cannot be read.
			name:     "a changed file that cannot be read",
			baseline: `mkdir -p coverage && printf 'SF:src/b.ts\nDA:1,1\nLF:1\nLH:1\nend_of_record\n' > coverage/lcov.info`,
			fixture: func(t *testing.T, f *webFixture) {
				writeFile(t, f.root, "web/src/b.ts/.keep", "")
				f.changed = map[string][]int{"web/src/b.ts": {1}}
			},
			kind: KindGenerateFailed,
			check: func(t *testing.T, o ComponentOutcome) {
				if o.Err == nil {
					t.Error("a source that could not be read carried no error")
				}
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newWebFixture(t)
			if c.fixture != nil {
				c.fixture(t, f)
			}
			lifecycle := f.lifecycle(t)
			if c.edit != nil {
				c.edit(lifecycle)
			}
			in := f.in(f.shape(t, c.baseline, "true", "true"), lifecycle)
			in.Memory = c.memory
			o := only(t, in)

			if o.Kind != c.kind {
				t.Fatalf("kind = %s (%v), want %s", o.Kind, o.Err, c.kind)
			}
			if c.err != nil && !errors.Is(o.Err, c.err) {
				t.Errorf("err = %v, want %v", o.Err, c.err)
			}
			if o.Ran() || len(o.Results) != 0 {
				t.Error("a component that stopped reports mutants that ran")
			}
			if c.check != nil {
				c.check(t, o)
			}
			if c.want != nil {
				got := f.calls.list()
				got = got[1 : len(got)-1]
				want := make([]string, len(c.want))
				for i, w := range c.want {
					want[i] = strings.ReplaceAll(w, "ROOT", f.root)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
				}
			}
		})
	}
}

// A teardown that failed is carried beside whatever became of the component,
// never in place of it: which of the two a report leads with is the command's
// call.
func TestATeardownFailureIsCarriedBesideTheOutcome(t *testing.T) {
	f := newWebFixture(t)
	lifecycle := f.lifecycle(t)
	left := errors.New("the stack is still running")
	lifecycle.runCommands = func(_ context.Context, _, _, kind string, _ []string, _ *toolchain.Env) error {
		if kind == "teardown" {
			return left
		}
		return nil
	}
	o := only(t, f.in(f.shape(t, lcovExecuting, "true", "true"), lifecycle))
	if o.Kind != KindCompleted {
		t.Fatalf("kind = %s (%v), want the completed measurement kept", o.Kind, o.Err)
	}
	if !errors.Is(o.TeardownErr, left) {
		t.Errorf("teardown = %v, want the teardown's own failure", o.TeardownErr)
	}
}

// A run cancelled before a component's baseline could take a slot reports
// that, and the component's teardown still runs — out from under the
// cancellation, since a cancelled teardown is the leak it exists to prevent.
func TestARunInterruptedBeforeTheBaselineSaysSo(t *testing.T) {
	f := newWebFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lifecycle := f.lifecycle(t)
	var teardownCancelled bool
	lifecycle.runCommands = func(ctx context.Context, _, _, kind string, _ []string, _ *toolchain.Env) error {
		switch kind {
		case "setup":
			cancel()
		case "teardown":
			teardownCancelled = ctx.Err() != nil
		}
		return nil
	}
	in := f.in(f.shape(t, lcovExecuting, "true", "true"), lifecycle)
	// No bound on suite executions, so the baseline's slot is refused on the
	// cancellation alone rather than on which of two ready cases a select
	// happened to pick.
	in.Limit = 1 << 20
	out, err := RunMutants(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	o := out.Components[0]
	if o.Kind != KindBaselineInterrupted {
		t.Fatalf("kind = %s (%v), want baseline-interrupted", o.Kind, o.Err)
	}
	if !out.Interrupted || !o.Scheduled {
		t.Errorf("interrupted %v, scheduled %v, want both", out.Interrupted, o.Scheduled)
	}
	if o.Component.Name != "web" || o.LogRel != ".lydite-reports/web/mutation.log" {
		t.Errorf("outcome names %q with log %q, want the component's own", o.Component.Name, o.LogRel)
	}
	if teardownCancelled {
		t.Error("the teardown ran under the cancelled context")
	}
}

// An interrupt keeps every fact of a scheduled outcome, a completed one's
// included: which verdicts still stand is the caller's to decide, and a
// component that finished before the interrupt has a verdict to keep.
func TestAnInterruptKeepsWhatAScheduledComponentReached(t *testing.T) {
	f := newWebFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lifecycle := f.lifecycle(t)
	lifecycle.runCommands = func(_ context.Context, _, _, kind string, _ []string, _ *toolchain.Env) error {
		if kind == "teardown" {
			cancel()
		}
		return nil
	}
	out, err := RunMutants(ctx, f.in(f.shape(t, lcovExecuting, "true", "true"), lifecycle))
	if err != nil {
		t.Fatal(err)
	}
	o := out.Components[0]
	if o.Kind != KindCompleted || !o.Ran() {
		t.Fatalf("kind = %s (%v), want completed", o.Kind, o.Err)
	}
	if !out.Interrupted || !o.Scheduled {
		t.Errorf("interrupted %v, scheduled %v, want both", out.Interrupted, o.Scheduled)
	}
	if len(o.Results) == 0 || o.Summary.Survived != len(o.Results) || !reflect.DeepEqual(o.Scoped, f.changed) || o.Elapsed <= 0 {
		t.Errorf("summary %+v over %d result(s), scoped %v, elapsed %v, want what the run reached kept",
			o.Summary, len(o.Results), o.Scoped, o.Elapsed)
	}
}

// A component that cannot be scheduled keeps the reason its plan gave, is
// never started, and is never scheduled: its account was final before the run
// began. One declaring no suite is not counted among the suites.
func TestAComponentThatCouldNotBePlannedIsNeverStarted(t *testing.T) {
	root := t.TempDir()
	unplanned := errors.New("the compose file would not load")
	app := component.Component{Name: "app", Dir: "app", Runner: "go-test"}
	scripts := component.Component{Name: "scripts", Dir: "scripts"}
	web := component.Component{Name: "web", Dir: "web", Runner: "vitest"}
	lifecycle := &fakeLifecycle{t: t,
		plan: func(context.Context, string, []component.Component, bool) []Planned {
			return []Planned{
				{Component: app, NotReady: unplanned},
				{Component: scripts, NotReady: rowError{detail: []string{"declares no suite"}}},
				{Component: web, Ready: true, Item: scheduler.Item{Name: "web", Dir: "web"}},
			}
		},
		close: func() {},
	}
	shape := &fakeShape{t: t, noSuite: func(c component.Component) bool { return c.Name == "scripts" }}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := RunMutants(ctx, RunMutantsIn{Shape: shape, Lifecycle: lifecycle, Dir: root,
		Selected: []component.Component{app, scripts, web}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Suites != 2 {
		t.Errorf("suites = %d, want the two components declaring one", out.Suites)
	}
	if got := []OutcomeKind{out.Components[0].Kind, out.Components[1].Kind, out.Components[2].Kind}; !reflect.DeepEqual(got,
		[]OutcomeKind{KindBlocked, KindBlocked, KindNotRun}) {
		t.Errorf("kinds = %v, want blocked, blocked, not-run in plan order", got)
	}
	if !errors.Is(out.Components[0].Err, unplanned) {
		t.Errorf("app's err = %v, want its plan's own", out.Components[0].Err)
	}
	if out.Components[0].Scheduled || out.Components[1].Scheduled || !out.Components[2].Scheduled {
		t.Errorf("scheduled = %v, %v, %v, want only the component whose plan was ready",
			out.Components[0].Scheduled, out.Components[1].Scheduled, out.Components[2].Scheduled)
	}
	if !out.Interrupted || out.Schedule.Started != 0 {
		t.Errorf("interrupted %v after %d started, want an interrupted run that started nothing", out.Interrupted, out.Schedule.Started)
	}
}

// The logs are closed once the scheduler is done with every component, and
// not before: a component's own lines are still being written until then.
func TestTheLogsAreClosedOnceEveryComponentIsDone(t *testing.T) {
	f := newWebFixture(t)
	lifecycle := f.lifecycle(t)
	var closedEarly bool
	closed := false
	lifecycle.close = func() { closed = true }
	down := lifecycle.startServices
	lifecycle.startServices = func(ctx context.Context, name string) (func(), error) {
		stop, err := down(ctx, name)
		return func() {
			closedEarly = closed
			stop()
		}, err
	}
	only(t, f.in(f.shape(t, lcovExecuting, "true", "true"), lifecycle))
	if !closed || closedEarly {
		t.Errorf("closed %v, before the component finished %v", closed, closedEarly)
	}
}

// Present, so ADR 0026's completeness rule holds with no exception and the
// fold needs no second copy of the opt-out rule. Nothing about the component
// is asked, prepared or run.
func TestAComponentThatOptedOutStillTakesAnOutcomeAndRunsNothing(t *testing.T) {
	off := false
	c := component.Component{Name: "app", Dir: ".", Runner: "go-test", Mutation: &off}
	o := mutateComponent(t.Context(), RunMutantsIn{Shape: &fakeShape{t: t}, Lifecycle: &fakeLifecycle{t: t}, Dir: t.TempDir()},
		Planned{Component: c, LogRel: "log"}, nil, nil)
	if o.Kind != KindMutationOff || o.Component.Name != "app" || o.LogRel != "log" {
		t.Errorf("outcome = %+v, want mutation-off for app", o)
	}
	if o.Ran() {
		t.Error("an opted-out component was run")
	}
}

// A component the change touches no source of is refused before anything is
// prepared, started or run. Half of what bounds a mutant is knowable from the
// diff alone, so the baseline suite, the services and the setup commands are
// pure cost there — and on the default branch, where HEAD is its own
// merge-base, that is every component.
//
// Asserted on the refusal itself and not only on the outcome: a decision that
// reported the outcome and carried on would run the whole component anyway,
// and the Lifecycle here fails the test on any call.
func TestAnUntouchedComponentRunsNothing(t *testing.T) {
	c := component.Component{Name: "app", Dir: "cli", Runner: "go-test"}
	shape := &fakeShape{t: t,
		lang:       runnerLang,
		invocation: func(component.Component, runner.Variant) (runner.Invocation, error) { return runner.Invocation{}, nil },
		scope: func(changed map[string][]int, _ component.Component) map[string][]int {
			if _, ok := changed["web/app.ts"]; !ok {
				t.Errorf("scoped %v, want the run's own changed lines", changed)
			}
			return nil
		},
	}
	in := RunMutantsIn{Shape: shape, Lifecycle: &fakeLifecycle{t: t}, Dir: t.TempDir(), Changed: map[string][]int{"web/app.ts": {1}}}
	o := mutateComponent(t.Context(), in, Planned{Component: c}, nil, nil)
	if o.Kind != KindUntouched {
		t.Fatalf("kind = %s, want untouched", o.Kind)
	}
	if o.Ran() {
		t.Error("an untouched component was run")
	}
}

// Every way a component cannot be mutated at all is settled from its
// declaration and the diff before anything is prepared, started or run.
func TestAComponentThatCannotBeMutatedIsRefusedFromItsDeclaration(t *testing.T) {
	derivable := func(component.Component, runner.Variant) (runner.Invocation, error) {
		return runner.Invocation{Name: "go", CoverageReport: "cover.out"}, nil
	}
	touched := func(changed map[string][]int, _ component.Component) map[string][]int { return changed }
	noVariant := errors.New(`runner "go-test" supplies no build-only variant`)
	for _, c := range []struct {
		name  string
		c     component.Component
		shape func(t *testing.T) *fakeShape
		files []string
		kind  OutcomeKind
		err   string
	}{
		{
			name:  "a raw command",
			c:     component.Component{Name: "app", Dir: "app", Command: []string{"make", "test"}},
			shape: func(t *testing.T) *fakeShape { return &fakeShape{t: t, lang: runnerLang} },
			kind:  KindRawCommand,
		},
		{
			name: "a runner implying no language",
			c:    component.Component{Name: "app", Dir: "app"},
			shape: func(t *testing.T) *fakeShape {
				return &fakeShape{t: t, lang: func(component.Component) runner.Lang { return "" }}
			},
			kind: KindRawCommand,
		},
		{
			name: "a variant that cannot be derived",
			c:    component.Component{Name: "app", Dir: "app", Runner: "go-test"},
			shape: func(t *testing.T) *fakeShape {
				return &fakeShape{t: t, lang: runnerLang,
					invocation: func(_ component.Component, v runner.Variant) (runner.Invocation, error) {
						if v == runner.BuildOnly {
							return runner.Invocation{}, noVariant
						}
						return runner.Invocation{}, nil
					}}
			},
			kind: KindInvocationFailed, err: noVariant.Error(),
		},
		{
			name: "a worker directory with nothing to copy",
			c:    component.Component{Name: "web", Dir: "web", Runner: "vitest"},
			shape: func(t *testing.T) *fakeShape {
				return &fakeShape{t: t, lang: runnerLang, invocation: derivable}
			},
			kind: KindNoBackend, err: "nothing to copy",
		},
		{
			name: "an instrumented variant naming no report",
			c:    component.Component{Name: "app", Dir: "app", Runner: "go-test"},
			shape: func(t *testing.T) *fakeShape {
				return &fakeShape{t: t, lang: runnerLang, scope: touched,
					invocation: func(component.Component, runner.Variant) (runner.Invocation, error) {
						return runner.Invocation{Name: "go"}, nil
					}}
			},
			kind: KindNoCoverageReport,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := RunMutantsIn{Shape: c.shape(t), Lifecycle: &fakeLifecycle{t: t}, Dir: t.TempDir(),
				Changed: map[string][]int{"app/a.go": {1}}, Files: c.files}
			_, o, ok := prepareTarget(in, Planned{Component: c.c}, nil)
			if ok {
				t.Fatal("a component that cannot be mutated was prepared to run")
			}
			if o.Kind != c.kind {
				t.Errorf("kind = %s, want %s", o.Kind, c.kind)
			}
			if c.err == "" && o.Err != nil {
				t.Errorf("err = %v, want none", o.Err)
			}
			if c.err != "" && (o.Err == nil || !strings.Contains(o.Err.Error(), c.err)) {
				t.Errorf("err = %v, want it to say %q", o.Err, c.err)
			}
		})
	}
}

// A component that can be mutated is handed everything its run needs, with
// its directory joined onto the scan root.
func TestAComponentThatCanBeMutatedIsPreparedWithItsOwnDirectory(t *testing.T) {
	root := t.TempDir()
	shape := &fakeShape{t: t, lang: runnerLang,
		invocation: func(_ component.Component, v runner.Variant) (runner.Invocation, error) {
			return runner.Invocation{Name: string(v), CoverageReport: "cover.out"}, nil
		},
		scope: func(changed map[string][]int, _ component.Component) map[string][]int { return changed },
	}
	c := component.Component{Name: "app", Dir: "app", Runner: "go-test"}
	changed := map[string][]int{"app/a.go": {1}}
	tg, _, ok := prepareTarget(RunMutantsIn{Shape: shape, Lifecycle: &fakeLifecycle{t: t}, Dir: root, Changed: changed},
		Planned{Component: c}, nil)
	if !ok {
		t.Fatal("a component the change touches was refused")
	}
	if tg.dir != filepath.Join(root, "app") || tg.lang != runner.Go {
		t.Errorf("dir %q in %q, want %q in go", tg.dir, tg.lang, filepath.Join(root, "app"))
	}
	if tg.inv.Name != string(runner.Instrumented) || tg.suite.Name != string(runner.Plain) {
		t.Errorf("baseline %q and suite %q, want the instrumented and the plain variants", tg.inv.Name, tg.suite.Name)
	}
	backend, isGo := tg.backend.(mutation.Go)
	if !isGo || backend.Build.Name != string(runner.BuildOnly) {
		t.Errorf("backend = %+v, want the overlay building with the build-only variant", tg.backend)
	}
	if !reflect.DeepEqual(tg.scoped, changed) {
		t.Errorf("scoped = %v, want %v", tg.scoped, changed)
	}
}

// Every kind has a name of its own, so a kind that reaches a report or a log
// is never an anonymous number.
func TestEveryOutcomeKindIsNamed(t *testing.T) {
	seen := map[string]OutcomeKind{}
	for k := KindNotRun; k <= KindIncomplete; k++ {
		name := k.String()
		if strings.HasPrefix(name, "OutcomeKind(") {
			t.Errorf("kind %d has no name", int(k))
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("kinds %d and %d are both %q", int(prev), int(k), name)
		}
		seen[name] = k
	}
	if got := OutcomeKind(0).String(); got != "OutcomeKind(0)" {
		t.Errorf("the zero kind is %q, want it named as no kind", got)
	}
}

// Each kind's name is pinned exactly, not merely present: a fold or a log
// line quotes this string verbatim, so a kind silently renamed to empty would
// still be unique among the others and pass a check that only asked for that.
func TestEachOutcomeKindsNameIsPinned(t *testing.T) {
	for kind, want := range map[OutcomeKind]string{
		KindNotRun:              "not-run",
		KindBlocked:             "blocked",
		KindMutationOff:         "mutation-off",
		KindRawCommand:          "raw-command",
		KindInvocationFailed:    "invocation-failed",
		KindNoBackend:           "no-backend",
		KindUntouched:           "untouched",
		KindNoCoverageReport:    "no-coverage-report",
		KindClearReportFailed:   "clear-report-failed",
		KindBaselineInterrupted: "baseline-interrupted",
		KindBaselineFailed:      "baseline-failed",
		KindBaselineTooLarge:    "baseline-too-large",
		KindMeasureFailed:       "measure-failed",
		KindGenerateFailed:      "generate-failed",
		KindNothingToMutate:     "nothing-to-mutate",
		KindExecuteFailed:       "execute-failed",
		KindCompleted:           "completed",
		KindIncomplete:          "incomplete",
	} {
		if got := kind.String(); got != want {
			t.Errorf("kind %d is named %q, want %q", int(kind), got, want)
		}
	}
}

// resumable is the fixture's run with resume on over stateDir, whose baseline
// and suite each append a line to a counter file per execution, and whose
// suite kills every mutant. env is what the fake Shape composes for every
// command.
func (f *webFixture) resumable(t *testing.T, stateDir string, env []string) (RunMutantsIn, func() (baselines, suites int)) {
	t.Helper()
	counters := t.TempDir()
	baselineCount := filepath.Join(counters, "baseline")
	suiteCount := filepath.Join(counters, "suite")
	shape := f.shape(t, "echo x >> '"+baselineCount+"' && "+lcovExecuting, "true", "echo x >> '"+suiteCount+"'; exit 1")
	shape.env = func(*toolchain.Env, component.Component, runner.Invocation) []string { return env }
	in := f.in(shape, f.lifecycle(t))
	in.StateDir, in.TreeDigest, in.LyditeVersion = stateDir, "tree-a", "v1.2.3"
	lines := func(path string) int {
		data, err := os.ReadFile(path) // #nosec G304 -- a counter inside the test's own temporary directory
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}
	return in, func() (int, int) { return lines(baselineCount), lines(suiteCount) }
}

// runToSummary runs in and returns its one outcome, failing the test unless its
// mutants ran to a summary.
func runToSummary(t *testing.T, in RunMutantsIn) ComponentOutcome {
	t.Helper()
	o := only(t, in)
	if o.Kind != KindCompleted {
		t.Fatalf("kind = %s (%v), want completed", o.Kind, o.Err)
	}
	return o
}

// A second run over the same tree answers every mutant from what the first
// recorded: no baseline and no suite runs again, and the counts are the ones
// the first run measured.
func TestASecondRunOverTheSameTreeMeasuresNothingAgain(t *testing.T) {
	f := newWebFixture(t)
	in, counts := f.resumable(t, t.TempDir(), []string{"TOKEN=a"})

	first := runToSummary(t, in)
	baselines, suites := counts()
	if baselines != 1 || suites == 0 {
		t.Fatalf("the first run ran %d baseline(s) and %d suite(s), want one and at least one", baselines, suites)
	}

	second := runToSummary(t, in)
	if b, s := counts(); b != baselines || s != suites {
		t.Errorf("the second run ran %d baseline(s) and %d suite(s) more, want none", b-baselines, s-suites)
	}
	if second.Summary != first.Summary {
		t.Errorf("summary = %+v, want the first run's %+v", second.Summary, first.Summary)
	}
	if !reflect.DeepEqual(second.Results, first.Results) {
		t.Errorf("results =\n  %+v\nwant the first run's\n  %+v", second.Results, first.Results)
	}
}

// Reused counts the results a resumed run answered from what was recorded: none
// on the run that measured them, and every one the second run had a verdict for.
func TestAResumedRunCountsWhatItReused(t *testing.T) {
	f := newWebFixture(t)
	in, _ := f.resumable(t, t.TempDir(), nil)

	first := runToSummary(t, in)
	if first.Reused != 0 {
		t.Errorf("the run that measured everything reused %d verdict(s)", first.Reused)
	}
	second := runToSummary(t, in)
	if second.Reused != len(second.Results) || second.Reused == 0 {
		t.Errorf("the second run reused %d of %d verdict(s), want all of them", second.Reused, len(second.Results))
	}
}

// An acknowledged mutant is answered by its declaration whatever a recorded
// verdict says, so it is never counted as reused.
func TestAnAcknowledgedMutantIsNeverCountedAsReused(t *testing.T) {
	plain := mutation.Mutant{Path: "a.go", Operator: mutation.NegateConditional, Original: "<", Mutated: ">="}
	declared := mutation.Mutant{Path: "b.go", Operator: mutation.NegateConditional, Original: "<", Mutated: ">=", Reason: "equivalent"}
	unrecorded := mutation.Mutant{Path: "c.go", Operator: mutation.NegateConditional, Original: "<", Mutated: ">="}
	known := map[string]mutation.Result{
		mutation.MutantID(plain):    {Outcome: mutation.Killed},
		mutation.MutantID(declared): {Outcome: mutation.Survived},
	}
	if got := reusedCount([]mutation.Mutant{plain, declared, unrecorded}, known); got != 1 {
		t.Errorf("reused = %d, want only the unacknowledged mutant with a recorded verdict", got)
	}
	if got := reusedCount([]mutation.Mutant{plain}, nil); got != 0 {
		t.Errorf("reused = %d with nothing recorded", got)
	}
}

// A recorded baseline saves the suite and nothing around it: the second run
// still prepares the component, brings its services up and runs its setup and
// teardown, because every mutant it dispatches needs them.
func TestABaselineHitStillPreparesTheComponent(t *testing.T) {
	f := newWebFixture(t)
	in, counts := f.resumable(t, t.TempDir(), nil)
	runToSummary(t, in)
	before := len(f.calls.list())

	runToSummary(t, in)
	if b, _ := counts(); b != 1 {
		t.Fatalf("%d baseline(s) ran over two runs, want the second to reuse the first's", b)
	}
	second := f.calls.list()[before:]
	for _, want := range []string{"clear coverage/lcov.info", "prepare web root=" + f.root, "services web", "setup web", "teardown web", "stop web"} {
		if !contains(second, want) {
			t.Errorf("the second run's calls %q lack %q", second, want)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Anything a verdict depends on changing re-runs everything: the tree, the
// platform, and the value of a variable the suite runs under.
func TestAChangedInputToAVerdictMeasuresEverythingAgain(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, in *RunMutantsIn, env []string)
	}{
		{"tree", func(_ *testing.T, in *RunMutantsIn, _ []string) { in.TreeDigest = "tree-b" }},
		{"platform", func(t *testing.T, _ *RunMutantsIn, _ []string) {
			was := platform
			platform = "plan9/mips"
			t.Cleanup(func() { platform = was })
		}},
		{"environment", func(_ *testing.T, _ *RunMutantsIn, env []string) { env[0] = "TOKEN=b" }},
		{"lydite", func(_ *testing.T, in *RunMutantsIn, _ []string) { in.LyditeVersion = "v1.2.4" }},
		{"timeout", func(_ *testing.T, in *RunMutantsIn, _ []string) { in.Timeout = time.Minute }},
		{"memory", func(_ *testing.T, in *RunMutantsIn, _ []string) { in.Memory = 1 << 30 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newWebFixture(t)
			env := []string{"TOKEN=a"}
			in, counts := f.resumable(t, t.TempDir(), env)
			first := runToSummary(t, in)
			baselines, suites := counts()

			c.change(t, &in, env)
			second := runToSummary(t, in)
			if b, s := counts(); b != 2*baselines || s != 2*suites {
				t.Errorf("the second run ran %d baseline(s) and %d suite(s), want %d and %d", b-baselines, s-suites, baselines, suites)
			}
			if second.Summary != first.Summary {
				t.Errorf("summary = %+v, want %+v", second.Summary, first.Summary)
			}
		})
	}
}

// A declared variable's value moves the fingerprint and is never written
// anywhere the state keeps.
func TestTheComposedEnvironmentIsKeptOnlyAsAHash(t *testing.T) {
	f := newWebFixture(t)
	stateDir := t.TempDir()
	in, _ := f.resumable(t, stateDir, []string{"TOKEN=s3cr3t-value"})
	runToSummary(t, in)
	err := filepath.WalkDir(stateDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 -- a file inside the test's own temporary directory
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "s3cr3t-value") {
			t.Errorf("%s carries the declared value", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The environment's digest reads the list the way a child does: the last
// occurrence of a key wins, and order is otherwise irrelevant.
func TestTheEnvironmentDigestReadsTheListAsAChildDoes(t *testing.T) {
	if envDigest([]string{"A=1", "B=2"}) != envDigest([]string{"B=2", "A=1"}) {
		t.Error("two orders of the same variables hash differently")
	}
	if envDigest([]string{"A=1", "A=2"}) == envDigest([]string{"A=2", "A=1"}) {
		t.Error("two lists a child reads differently hash the same")
	}
	if envDigest([]string{"A=1", "A=2"}) != envDigest([]string{"A=2"}) {
		t.Error("a shadowed occurrence moves the digest")
	}
}

// A state that cannot be written is a cache that is not there: the run
// measures everything, completes, and says so once.
func TestAnUnwritableStateMeasuresEverythingAndWarnsOnce(t *testing.T) {
	f := newWebFixture(t)
	blocked := filepath.Join(t.TempDir(), "blocked")
	writeFile(t, filepath.Dir(blocked), "blocked", "")
	var diagnostics lockedBuffer
	in, counts := f.resumable(t, filepath.Join(blocked, "state"), nil)
	in.Diagnostics = &diagnostics

	first := runToSummary(t, in)
	runToSummary(t, in)
	if b, s := counts(); b != 2 || s != 2*first.Summary.Killed {
		t.Errorf("ran %d baseline(s) and %d suite(s) over two runs, want every one measured twice", b, s)
	}
	lines := strings.Split(strings.TrimSpace(diagnostics.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("diagnostics = %q, want one line per run", diagnostics.String())
	}
	if !strings.HasPrefix(lines[0], "warning: web's mutation state: ") {
		t.Errorf("diagnostic = %q", lines[0])
	}
}

// A verdict that cannot be recorded is named once however many mutants fail
// to record, and costs the run nothing.
func TestAVerdictThatCannotBeRecordedIsNamedOnce(t *testing.T) {
	var diagnostics lockedBuffer
	s := &componentState{state: closedState(t), name: "web", diagnostics: &diagnostics}
	record := s.recorder()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(mutation.Result{Mutant: mutation.Mutant{Path: "a.ts"}, Outcome: mutation.Killed})
		}()
	}
	wg.Wait()
	if n := strings.Count(diagnostics.String(), "\n"); n != 1 {
		t.Errorf("diagnostics = %q, want one line", diagnostics.String())
	}
}

// closedState is a state every Record refuses.
func closedState(t *testing.T) *mutation.State {
	t.Helper()
	s, err := mutation.OpenState(t.TempDir(), "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return s
}

// Each way a component's state can fail while it is used is named once, with
// what the run does instead, and is never an error the run has to handle.
func TestAComponentStateThatFailsNamesItAndFallsBack(t *testing.T) {
	newState := func(t *testing.T) (*componentState, string, *lockedBuffer) {
		t.Helper()
		dir := t.TempDir()
		st, err := mutation.OpenState(dir, "fp")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		diag := &lockedBuffer{}
		return &componentState{state: st, name: "web", diagnostics: diag}, dir, diag
	}
	occupy := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, "occupant", "x")
	}
	wantWarning := func(t *testing.T, diag *lockedBuffer) {
		t.Helper()
		if got := diag.String(); !strings.HasPrefix(got, "warning: web's mutation state: ") || strings.Count(got, "\n") != 1 {
			t.Errorf("diagnostics = %q, want one warning naming the component's state", got)
		}
	}

	t.Run("a baseline that cannot be read", func(t *testing.T) {
		s, dir, diag := newState(t)
		occupy(t, filepath.Join(dir, "baseline.json"))
		if _, ok := s.baseline(); ok {
			t.Error("an unreadable baseline was reused")
		}
		wantWarning(t, diag)
	})
	t.Run("a baseline that cannot be saved", func(t *testing.T) {
		s, dir, diag := newState(t)
		occupy(t, filepath.Join(dir, "baseline.json"))
		s.saveBaseline(mutation.Baseline{Passed: true})
		wantWarning(t, diag)
	})
	t.Run("verdicts that cannot be read", func(t *testing.T) {
		s, dir, diag := newState(t)
		if err := os.Remove(filepath.Join(dir, "verdicts.jsonl")); err != nil {
			t.Fatal(err)
		}
		if known := s.verdicts(); known != nil {
			t.Errorf("verdicts = %v, want none", known)
		}
		wantWarning(t, diag)
	})
	t.Run("a state with none to use", func(t *testing.T) {
		var none *componentState
		if _, ok := none.baseline(); ok {
			t.Error("a run without state reused a baseline")
		}
		none.saveBaseline(mutation.Baseline{})
		if none.verdicts() != nil || none.recorder() != nil {
			t.Error("a run without state answered from one")
		}
		none.close()
	})
}

// Fresh discards what was recorded, so a run asking for it measures
// everything again.
func TestAFreshRunDiscardsTheState(t *testing.T) {
	f := newWebFixture(t)
	in, counts := f.resumable(t, t.TempDir(), nil)
	runToSummary(t, in)
	baselines, suites := counts()

	in.Fresh = true
	runToSummary(t, in)
	if b, s := counts(); b != 2*baselines || s != 2*suites {
		t.Errorf("the fresh run ran %d baseline(s) and %d suite(s), want %d and %d", b-baselines, s-suites, baselines, suites)
	}
}

// Resume is off without a tree digest: nothing is recorded, and a second run
// measures everything.
func TestWithoutATreeDigestNothingIsResumed(t *testing.T) {
	f := newWebFixture(t)
	stateDir := t.TempDir()
	in, counts := f.resumable(t, stateDir, nil)
	in.TreeDigest = ""
	runToSummary(t, in)
	runToSummary(t, in)
	if b, _ := counts(); b != 2 {
		t.Errorf("%d baseline(s) over two runs, want two", b)
	}
	if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
		t.Errorf("the state root holds %d entr(ies) (%v), want none", len(entries), err)
	}
}

// countingSuite is a suite that appends a line to count on every execution
// and kills the mutant, except on every even-numbered execution, where it
// hangs until something kills it.
func countingSuite(count string) string {
	return "echo x >> '" + count + "'; n=$(wc -l < '" + count + "'); " +
		"if [ $((n % 2)) -eq 0 ]; then sleep 30; fi; exit 1"
}

// twoComparisons rewrites the fixture's source to hold a comparison on each of
// two changed, executed lines, which is more mutants than one line yields.
func (f *webFixture) twoComparisons(t *testing.T) {
	t.Helper()
	writeFile(t, f.root, "web/src/a.ts", webSource+"export function more(x: number, y: number): boolean {\n  return x > y;\n}\n")
	f.changed = map[string][]int{"web/src/a.ts": {2, 5}}
}

// lcovBothExecuting is the baseline reporting both of twoComparisons' lines
// executed.
const lcovBothExecuting = `mkdir -p coverage && printf 'SF:src/a.ts\nDA:2,1\nDA:5,1\nLF:2\nLH:2\nend_of_record\n' > coverage/lcov.info`

// deadlineFixture is the fixture's run with resume on, a suite that decides
// one mutant and hangs on the next, and a per-mutant budget no hang reaches
// before the deadline does.
func (f *webFixture) deadlineFixture(t *testing.T) RunMutantsIn {
	t.Helper()
	in := f.in(f.shape(t, lcovBothExecuting, "true", countingSuite(filepath.Join(t.TempDir(), "suite"))), f.lifecycle(t))
	in.StateDir, in.TreeDigest, in.LyditeVersion = t.TempDir(), "tree-a", "v1.2.3"
	in.Timeout = time.Hour
	return in
}

// runUntil runs in under a deadline d from now, and returns the run and its
// one outcome.
func runUntil(t *testing.T, ctx context.Context, in RunMutantsIn, d time.Duration) (RunMutantsOut, ComponentOutcome) {
	t.Helper()
	in.Deadline = time.Now().Add(d)
	out, err := RunMutants(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Components) != 1 {
		t.Fatalf("%d outcome(s), want one", len(out.Components))
	}
	return out, out.Components[0]
}

// A deadline that lands while a mutant is in flight cancels it, dispatches
// nothing after it, and answers incomplete: measured counts every mutant with
// a verdict — this run's and the ones reused from the last — and wanted every
// mutant generated. What the deadline cut short is in neither the count, the
// summary nor the results, and a rerun measures exactly it.
func TestADeadlineMidRunIsIncompleteAndCountsWhatHasAVerdict(t *testing.T) {
	f := newWebFixture(t)
	f.twoComparisons(t)
	in := f.deadlineFixture(t)

	out, first := runUntil(t, t.Context(), in, 3*time.Second)
	if first.Kind != KindIncomplete {
		t.Fatalf("kind = %s (%v), want incomplete", first.Kind, first.Err)
	}
	if !out.DeadlineReached || out.Interrupted {
		t.Errorf("deadline reached %v, interrupted %v, want only the deadline", out.DeadlineReached, out.Interrupted)
	}
	if first.Wanted < 3 {
		t.Fatalf("wanted = %d, want at least three mutants for two runs to leave some unmeasured", first.Wanted)
	}
	if first.Measured != 1 || len(first.Results) != 1 || first.Summary.Killed != 1 || first.Summary.Total() != 1 {
		t.Errorf("measured %d over %d result(s), summary %+v, want the one mutant killed before the hang",
			first.Measured, len(first.Results), first.Summary)
	}
	for _, r := range first.Results {
		if r.CutShort {
			t.Errorf("a cut-short result is reported: %+v", r)
		}
	}
	if first.Reused != 0 || !reflect.DeepEqual(first.Scoped, f.changed) || first.Elapsed <= 0 {
		t.Errorf("reused %d, scoped %v, elapsed %v", first.Reused, first.Scoped, first.Elapsed)
	}

	_, second := runUntil(t, t.Context(), in, 3*time.Second)
	if second.Kind != KindIncomplete {
		t.Fatalf("kind = %s (%v), want incomplete", second.Kind, second.Err)
	}
	if second.Reused != 1 || second.Measured != 2 || second.Wanted != first.Wanted {
		t.Errorf("reused %d, measured %d of %d, want the first run's verdict reused and one more decided of %d",
			second.Reused, second.Measured, second.Wanted, first.Wanted)
	}
	if second.Summary.Killed != 2 || len(second.Results) != 2 {
		t.Errorf("summary %+v over %d result(s), want the two verdicts", second.Summary, len(second.Results))
	}
}

// A deadline the run finishes inside changes nothing: the component completes
// with every verdict, and the run reports no deadline reached.
func TestARunThatFinishesBeforeItsDeadlineCompletes(t *testing.T) {
	f := newWebFixture(t)
	out, o := runUntil(t, t.Context(), f.in(f.shape(t, lcovExecuting, "true", "exit 1"), f.lifecycle(t)), time.Hour)
	if o.Kind != KindCompleted || !o.Ran() {
		t.Fatalf("kind = %s (%v), want completed", o.Kind, o.Err)
	}
	if out.DeadlineReached || out.Interrupted {
		t.Errorf("deadline reached %v, interrupted %v, want neither", out.DeadlineReached, out.Interrupted)
	}
	if o.Measured != 0 || o.Wanted != 0 || o.Summary.Killed == 0 || o.Summary.Killed != len(o.Results) {
		t.Errorf("measured %d of %d, summary %+v over %d result(s), want every mutant killed and no incomplete counts",
			o.Measured, o.Wanted, o.Summary, len(o.Results))
	}
}

// An interrupt is not a deadline, even in a run that has one: the cut-short
// mutants stay among the results for the caller to withdraw the run by, and
// the component is completed rather than incomplete.
func TestAnInterruptUnderADeadlineIsStillAnInterrupt(t *testing.T) {
	f := newWebFixture(t)
	count := filepath.Join(t.TempDir(), "suite")
	in := f.in(f.shape(t, lcovExecuting, "true", countingSuite(count)), f.lifecycle(t))
	in.Timeout = time.Hour

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		// Once the second mutant is in flight, which is the one that hangs.
		for ctx.Err() == nil {
			if data, err := os.ReadFile(count); err == nil && strings.Count(string(data), "\n") >= 2 { // #nosec G304 -- a counter inside the test's own temporary directory
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	out, o := runUntil(t, ctx, in, time.Hour)
	if o.Kind != KindCompleted {
		t.Fatalf("kind = %s (%v), want completed", o.Kind, o.Err)
	}
	if !out.Interrupted || out.DeadlineReached {
		t.Errorf("interrupted %v, deadline reached %v, want only the interrupt", out.Interrupted, out.DeadlineReached)
	}
	cut := 0
	for _, r := range o.Results {
		if r.CutShort {
			cut++
		}
	}
	if cut == 0 || o.Measured != 0 || o.Wanted != 0 {
		t.Errorf("%d cut-short result(s), measured %d of %d, want the cut-short ones kept and no incomplete counts",
			cut, o.Measured, o.Wanted)
	}
}

// A deadline that lands before a component's baseline has an answer leaves
// the mutants uncounted, since none were generated: the component is the
// baseline interrupted, whichever step the deadline killed — the setup, or the
// baseline suite itself, which is not a baseline that failed.
func TestADeadlineBeforeTheBaselineAnswersIsTheBaselineInterrupted(t *testing.T) {
	t.Run("setup", func(t *testing.T) {
		f := newWebFixture(t)
		lifecycle := f.lifecycle(t)
		lifecycle.runCommands = func(ctx context.Context, _, _, kind string, _ []string, _ *toolchain.Env) error {
			if kind == "setup" {
				<-ctx.Done()
				return rowError{detail: []string{"setup killed"}}
			}
			return nil
		}
		out, o := runUntil(t, t.Context(), f.in(f.shape(t, lcovExecuting, "true", "true"), lifecycle), 200*time.Millisecond)
		if o.Kind != KindBaselineInterrupted || o.Err != nil {
			t.Fatalf("kind = %s (%v), want baseline-interrupted", o.Kind, o.Err)
		}
		if !out.DeadlineReached || out.Interrupted || !o.Scheduled {
			t.Errorf("deadline reached %v, interrupted %v, scheduled %v", out.DeadlineReached, out.Interrupted, o.Scheduled)
		}
	})
	t.Run("baseline", func(t *testing.T) {
		f := newWebFixture(t)
		_, o := runUntil(t, t.Context(), f.in(f.shape(t, "exec sleep 30", "true", "true"), f.lifecycle(t)), 500*time.Millisecond)
		if o.Kind != KindBaselineInterrupted || o.Output != "" {
			t.Fatalf("kind = %s, output %q, want baseline-interrupted with no failing output", o.Kind, o.Output)
		}
	})
	t.Run("already passed", func(t *testing.T) {
		f := newWebFixture(t)
		out, o := runUntil(t, t.Context(), f.in(f.shape(t, lcovExecuting, "true", "true"), f.lifecycle(t)), -time.Second)
		if o.Kind != KindNotRun || !out.DeadlineReached || out.Schedule.Started != 0 {
			t.Errorf("kind = %s, deadline reached %v, started %d, want nothing started", o.Kind, out.DeadlineReached, out.Schedule.Started)
		}
	})
}

// A step before the mutants are known that fails on its own merits, with the
// deadline still ahead, keeps its own kind.
func TestAFailureBeforeTheDeadlineKeepsItsOwnKind(t *testing.T) {
	f := newWebFixture(t)
	lifecycle := f.lifecycle(t)
	failed := errors.New("setup failed")
	lifecycle.runCommands = func(_ context.Context, _, _, kind string, _ []string, _ *toolchain.Env) error {
		if kind == "setup" {
			return failed
		}
		return nil
	}
	_, o := runUntil(t, t.Context(), f.in(f.shape(t, lcovExecuting, "true", "true"), lifecycle), time.Hour)
	if o.Kind != KindBlocked || !errors.Is(o.Err, failed) {
		t.Errorf("kind = %s (%v), want blocked by the setup's own failure", o.Kind, o.Err)
	}
}

// A deadline that stops a worker being prepared leaves nothing dispatched, and
// the component is incomplete over what was answered without running: its
// acknowledged mutants, whose declaration is their verdict.
func TestADeadlineThatStopsAWorkerCountsWhatNeededNoRun(t *testing.T) {
	f := newWebFixture(t)
	f.twoComparisons(t)
	writeFile(t, f.root, "web/src/a.ts", strings.Replace(webSource, "x < y;",
		"x < y; // [lydite:exclude_from_mutation][the comparison is exercised by an integration suite]", 1)+
		"export function more(x: number, y: number): boolean {\n  return x > y;\n}\n")
	lifecycle := f.lifecycle(t)
	lifecycle.prepare = func(ctx context.Context, _ string, _ runner.Invocation, _, root string, _ config.Config, _ *toolchain.Env) error {
		if root == "" {
			<-ctx.Done()
			return rowError{detail: []string{"npm ci killed"}}
		}
		return nil
	}
	_, o := runUntil(t, t.Context(), f.in(f.shape(t, lcovBothExecuting, "true", "true"), lifecycle), time.Second)
	if o.Kind != KindIncomplete {
		t.Fatalf("kind = %s (%v), want incomplete", o.Kind, o.Err)
	}
	if o.Measured == 0 || o.Measured >= o.Wanted || o.Summary.Acknowledged != o.Measured || len(o.Results) != o.Measured {
		t.Errorf("measured %d of %d, summary %+v over %d result(s), want only the acknowledged mutants measured",
			o.Measured, o.Wanted, o.Summary, len(o.Results))
	}
}

// What a worker the deadline stopped answers without running is the same
// answer the executor gives: an acknowledged mutant by its declaration, one a
// previous run recorded by its verdict with the mutant as generated, and no
// other mutant at all.
func TestWhatIsAnsweredWithoutRunningMatchesTheExecutor(t *testing.T) {
	acknowledged := mutation.Mutant{Path: "a.go", Line: 1, Offset: 10, Length: 1, Operator: mutation.NegateConditional,
		Original: "<", Mutated: ">=", Reason: "declared"}
	recorded := mutation.Mutant{Path: "a.go", Line: 2, Offset: 20, Length: 1, Operator: mutation.NegateConditional,
		Original: "<", Mutated: ">="}
	pending := mutation.Mutant{Path: "a.go", Line: 3, Offset: 30, Length: 1, Operator: mutation.NegateConditional,
		Original: "<", Mutated: ">="}
	mutants := []mutation.Mutant{acknowledged, recorded, pending}
	known := map[string]mutation.Result{mutation.MutantID(recorded): {Outcome: mutation.Killed}}

	got := answeredWithoutRunning(mutants, known)
	want, err := mutation.Execute(t.Context(), nil, mutants[:2], mutation.Options{Known: known})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("answered\n  %+v\nwant the executor's\n  %+v", got, want)
	}
}
