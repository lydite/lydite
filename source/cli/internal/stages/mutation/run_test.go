package mutationstages

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

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
	for k := KindNotRun; k <= KindCompleted; k++ {
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
