package mutationstages

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/toolchain"
)

// OutcomeKind is what became of one component: the step it stopped at, or
// that its mutants ran to a summary.
//
// One kind per distinct account a report could give of a component, so which
// row an outcome becomes is decided by its kind and its facts alone.
type OutcomeKind int

const (
	// KindNotRun is a component the scheduler never started: the run ended
	// before it.
	KindNotRun OutcomeKind = iota + 1
	// KindBlocked is a component whose lifecycle stopped it — a plan that
	// could not be made, or a preparation, a service or a setup command that
	// failed. Err is the Lifecycle's own error, and the account of it is the
	// one the Lifecycle already decided.
	KindBlocked
	// KindMutationOff is a component whose declaration turns mutation off.
	KindMutationOff
	// KindRawCommand is a component declaring a raw command, from which no
	// build-only or plain variant can be derived.
	KindRawCommand
	// KindInvocationFailed is a component one of whose three variants could
	// not be derived. Err says which and why.
	KindInvocationFailed
	// KindNoBackend is a component whose language has no way to isolate a
	// mutant, or no files to isolate one in. Err says which.
	KindNoBackend
	// KindUntouched is a component the change touches no source of.
	KindUntouched
	// KindNoCoverageReport is a component whose instrumented variant names
	// no coverage report, so there are no executed lines to mutate.
	KindNoCoverageReport
	// KindClearReportFailed is a component whose previous coverage report
	// could not be cleared. Err says why.
	KindClearReportFailed
	// KindBaselineInterrupted is a component the run was interrupted before
	// its baseline suite ran.
	KindBaselineInterrupted
	// KindBaselineFailed is a component whose baseline suite did not pass.
	// Output is what it printed.
	KindBaselineFailed
	// KindBaselineTooLarge is a component whose baseline suite held Peak
	// bytes, which leaves no room under the Bound its mutants would run at.
	KindBaselineTooLarge
	// KindMeasureFailed is a component whose coverage report could not be
	// read. Err says why.
	KindMeasureFailed
	// KindGenerateFailed is a component whose mutants could not be
	// generated. Err says why.
	KindGenerateFailed
	// KindNothingToMutate is a component with no line the change touched
	// that is both mutable and reported as executed.
	KindNothingToMutate
	// KindExecuteFailed is a component whose mutants could not be executed at
	// all — a worker that could not be opened or prepared. Err says why.
	KindExecuteFailed
	// KindCompleted is a component whose mutants ran to a summary.
	KindCompleted
)

func (k OutcomeKind) String() string {
	switch k {
	case KindNotRun:
		return "not-run"
	case KindBlocked:
		return "blocked"
	case KindMutationOff:
		return "mutation-off"
	case KindRawCommand:
		return "raw-command"
	case KindInvocationFailed:
		return "invocation-failed"
	case KindNoBackend:
		return "no-backend"
	case KindUntouched:
		return "untouched"
	case KindNoCoverageReport:
		return "no-coverage-report"
	case KindClearReportFailed:
		return "clear-report-failed"
	case KindBaselineInterrupted:
		return "baseline-interrupted"
	case KindBaselineFailed:
		return "baseline-failed"
	case KindBaselineTooLarge:
		return "baseline-too-large"
	case KindMeasureFailed:
		return "measure-failed"
	case KindGenerateFailed:
		return "generate-failed"
	case KindNothingToMutate:
		return "nothing-to-mutate"
	case KindExecuteFailed:
		return "execute-failed"
	case KindCompleted:
		return "completed"
	default:
		return fmt.Sprintf("OutcomeKind(%d)", int(k))
	}
}

// ComponentOutcome is what became of one component, and the facts its kind
// carries. A fact a kind does not name is zero.
type ComponentOutcome struct {
	Component component.Component
	// LogRel is the component's log relative to the scan root, as its plan
	// named it.
	LogRel string
	Kind   OutcomeKind
	// Err is why the component stopped, for every kind that names one.
	Err error
	// Output is what a failing baseline suite printed.
	Output string
	// Peak and Bound are a baseline's own peak memory and the bound its
	// mutants would have run at, in bytes.
	Peak, Bound int64
	// Summary, Results, Scoped and Elapsed are a completed component's: the
	// counts, what became of each mutant in generation order, the changed
	// lines its mutants came from, and how long the baseline and the mutants
	// took together. A KindNothingToMutate outcome carries Summary too, whose
	// only fact is Unmatched.
	Summary mutation.Summary
	Results []mutation.Result
	Scoped  map[string][]int
	Elapsed time.Duration
	// TeardownErr is the Lifecycle's error from the component's teardown
	// commands, which run whatever became of it once its services started.
	TeardownErr error
	// Scheduled reports that the component was handed to the scheduler, which
	// is every component whose plan was ready, whether or not the scheduler
	// started it. When the run is Interrupted these are the outcomes a failing
	// account of is in doubt — under cancellation a suite that failed cannot be
	// told from one that was killed — and an outcome that is not Scheduled was
	// final before the run began.
	Scheduled bool
}

// Ran reports whether the component's mutants were generated and executed to
// a summary.
func (o ComponentOutcome) Ran() bool { return o.Kind == KindCompleted }

// RunMutantsIn is everything the run decided before any component started.
type RunMutantsIn struct {
	Shape     Shape
	Lifecycle Lifecycle
	// Dir is the scan root.
	Dir      string
	Selected []component.Component
	Config   config.Config
	Envs     toolchain.Envs
	// Changed is every line the diff added, keyed by a path relative to the
	// scan root, and Files every path a worker directory is copied from.
	Changed map[string][]int
	Files   []string
	// Limit bounds suite executions in flight across the whole run.
	Limit int
	// Timeout and Memory override the budget and the memory bound derived
	// from each component's own baseline; zero asks for the derivation.
	Timeout time.Duration
	Memory  int64
	// Stream mirrors each component's log as it is written.
	Stream bool
	// Diagnostics is where a declaration that matched no mutant is named.
	Diagnostics io.Writer
	// StateDir is the resume state root, and empty when resume is off. Fresh
	// asks for what it holds to be ignored, LyditeVersion is the lydite that
	// wrote what is recorded, and TreeDigest is the digest of the tree the
	// run mutates.
	StateDir      string
	Fresh         bool
	LyditeVersion string
	TreeDigest    string
}

// RunMutantsOut is what became of every selected component, and of the run.
type RunMutantsOut struct {
	// Components is one outcome per selected component, in plan order.
	Components []ComponentOutcome
	// Schedule is what the scheduler did.
	Schedule scheduler.Outcome
	// Suites counts the selected components that declare a suite, which is
	// every component the scheduler could have been given.
	Suites int
	// Interrupted reports that the run was cancelled. Every outcome keeps its
	// facts either way: which of the Scheduled ones still stand is the
	// caller's to decide, since it is the caller that decides which account of
	// an outcome fails.
	Interrupted bool
}

// RunMutants plans every selected component, runs its mutants, and reports
// what became of each.
//
// The components go through the scheduler `lydite test` uses, under the same
// bound: a component is one item, so its services are started and torn down
// inside that item, and two components publishing one host port are
// serialised there rather than here. Inside each item its mutants dispatch
// against slots shared by the whole run, so Limit means suite executions in
// flight exactly as it does in `lydite test` — two independent bounds would
// multiply into components times mutants.
//
// A component the scheduler was given and never started is KindNotRun, never
// absent: a run cut short that dropped it would read as a complete run over
// fewer components.
func RunMutants(ctx context.Context, in RunMutantsIn) (RunMutantsOut, error) {
	if in.Diagnostics == nil {
		in.Diagnostics = io.Discard
	}
	plans := in.Lifecycle.Plan(ctx, in.Dir, in.Selected, in.Stream)

	out := RunMutantsOut{Components: make([]ComponentOutcome, len(plans))}
	var items []scheduler.Item
	var index []int
	for i, p := range plans {
		if !in.Shape.NoSuite(p.Component) {
			out.Suites++
		}
		o := ComponentOutcome{Component: p.Component, LogRel: p.LogRel}
		if !p.Ready {
			o.Kind, o.Err = KindBlocked, p.NotReady
			out.Components[i] = o
			continue
		}
		o.Kind = KindNotRun
		out.Components[i] = o
		items = append(items, p.Item)
		index = append(index, i)
	}

	slots := mutation.NewSlots(in.Limit)
	out.Schedule = scheduler.Run(ctx, items, in.Limit, func(ctx context.Context, k int) {
		i := index[k]
		out.Components[i] = mutateComponent(ctx, in, plans[i], in.Envs.For(plans[i].Component.Name), slots)
	})
	in.Lifecycle.Close()

	// After the run rather than before it, since a component the scheduler
	// started has its outcome replaced whole by what mutateComponent returned.
	for _, i := range index {
		out.Components[i].Scheduled = true
	}
	out.Interrupted = ctx.Err() != nil
	return out, nil
}

// target is everything a component needs before its first suite runs: the
// three invocations of its own declaration, the isolation strategy its mutants
// execute under, and the changed lines they may come from.
type target struct {
	lang    runner.Lang
	inv     runner.Invocation
	suite   runner.Invocation
	backend mutation.Backend
	scoped  map[string][]int
	dir     string
}

// prepareTarget answers whether this component can be mutated at all, and
// with what.
//
// Every way it cannot is an outcome rather than an error, and every one of
// them is settled from the declaration and the diff before anything is
// prepared, started or run. Half of what bounds a mutant is knowable that way,
// and a component the change does not touch has no mutant whatever its
// coverage says — so the baseline suite, the services and the setup commands
// are all pure cost there. On the default branch, where HEAD is its own
// merge-base, that is every component.
func prepareTarget(in RunMutantsIn, p Planned, tc *toolchain.Env) (target, ComponentOutcome, bool) {
	c := p.Component
	stopped := func(kind OutcomeKind, err error) (target, ComponentOutcome, bool) {
		return target{}, ComponentOutcome{Component: c, LogRel: p.LogRel, Kind: kind, Err: err}, false
	}
	var t target

	if !c.MutationEnabled() {
		return stopped(KindMutationOff, nil)
	}
	t.lang = in.Shape.Lang(c)
	if len(c.Command) > 0 || t.lang == "" {
		return stopped(KindRawCommand, nil)
	}
	// All three variants of one declaration, derived together: mutation needs
	// every one of them, and a component that cannot produce one can produce
	// no mutant at all.
	var build runner.Invocation
	for _, want := range []struct {
		variant runner.Variant
		into    *runner.Invocation
	}{
		{runner.Instrumented, &t.inv},
		{runner.BuildOnly, &build},
		{runner.Plain, &t.suite},
	} {
		inv, err := in.Shape.Invocation(c, want.variant)
		if err != nil {
			return stopped(KindInvocationFailed, err)
		}
		*want.into = inv
	}

	suite := t.suite
	backend, err := backendFor(t.lang, in.Dir, c.Dir, build, suite, in.Files,
		func(ctx context.Context, dir string) error {
			// The runner's own preparation, in the worker rather than in the
			// component: a JavaScript workspace copied without its
			// node_modules fails at import, naming the tests rather than the
			// absent dependencies. Once per worker and never once per mutant,
			// which is the bound that makes a tree copy affordable.
			//
			// No root of its own: dir is inside a copy of the scan root, not
			// the scan root itself, so the bound has to come from the copy's
			// own tree rather than from the repository it was copied from.
			//
			// The Lifecycle's error is returned as it is, so what the worker
			// reports is the Lifecycle's own account of the failure.
			return in.Lifecycle.Prepare(ctx, c.Name, suite, dir, "", in.Config, tc)
		})
	if err != nil {
		return stopped(KindNoBackend, err)
	}
	t.backend = backend

	t.scoped = in.Shape.Scope(in.Changed, c)
	if len(t.scoped) == 0 {
		return stopped(KindUntouched, nil)
	}
	// A runner whose instrumented variant names no report can supply no
	// executed lines, and mutation needs those as much as it needs a passing
	// baseline. Refused rather than attempted: clearing the report joins its
	// path onto the component's directory, so an empty one names the
	// directory itself, and asking to clear that is asking to remove the
	// component.
	if t.inv.CoverageReport == "" {
		return stopped(KindNoCoverageReport, nil)
	}
	t.dir = filepath.Join(in.Dir, filepath.FromSlash(c.Dir))
	return t, ComponentOutcome{}, true
}

// mutateComponent runs one component's baseline, generates its mutants and
// reports what became of them.
//
// The baseline is the instrumented variant, which is both halves of what this
// needs: a suite that passes, and the lines coverage says were executed. A
// component whose baseline fails is KindBaselineFailed rather than a verdict —
// nothing can be concluded about tests that were not passing before the
// mutation.
func mutateComponent(ctx context.Context, in RunMutantsIn, p Planned, tc *toolchain.Env, slots *mutation.Slots) (out ComponentOutcome) {
	c := p.Component
	out = ComponentOutcome{Component: c, LogRel: p.LogRel}
	t, stopped, ok := prepareTarget(in, p, tc)
	if !ok {
		return stopped
	}
	finish := func(kind OutcomeKind, err error) ComponentOutcome {
		out.Kind, out.Err = kind, err
		return out
	}

	if err := in.Lifecycle.ClearReport(t.dir, t.inv.CoverageReport); err != nil {
		return finish(KindClearReportFailed, err)
	}
	if err := in.Lifecycle.Prepare(ctx, c.Name, t.inv, t.dir, in.Dir, in.Config, tc); err != nil {
		return finish(KindBlocked, err)
	}
	down, err := in.Lifecycle.StartServices(ctx, c.Name)
	if err != nil {
		return finish(KindBlocked, err)
	}
	defer down()
	// Out from under the run's cancellation, because a cancelled teardown is
	// the leak it exists to prevent. Whatever became of the component, its
	// teardown's own failure is carried beside it rather than in place of it.
	defer func() {
		out.TeardownErr = in.Lifecycle.RunCommands(context.WithoutCancel(ctx), c.Name, t.dir, "teardown", c.Teardown, tc)
	}()
	if err := in.Lifecycle.RunCommands(ctx, c.Name, t.dir, "setup", c.Setup, tc); err != nil {
		return finish(KindBlocked, err)
	}

	env := in.Shape.Env(tc, c, t.inv)
	baselineStarted := time.Now()
	// Under the run's slots, because a baseline is a suite execution exactly
	// as a mutant is. Counting only mutants would let three components in
	// their baseline run beside a fourth executing four mutants — seven
	// suites in flight under a limit of four, which is what one bound exists
	// to prevent.
	var res executil.Result
	if !slots.Run(ctx, func() {
		res = executil.RunOutput(ctx, t.dir, env, p.Log, t.inv.Name, t.inv.Args...)
	}) {
		return finish(KindBaselineInterrupted, nil)
	}
	if !res.Ok() {
		out.Output = res.Output
		return finish(KindBaselineFailed, nil)
	}
	baseline := time.Since(baselineStarted)
	// Both halves of what bounds a mutant come off the same run: the elapsed
	// time and the peak the kernel reported for it. The baseline itself runs
	// under no ceiling, because deriving one needs the measurement first.
	maxMemory := memoryBudget(res.MaxRSS, in.Memory)
	if !memoryFits(res.MaxRSS, maxMemory) {
		// Gating nothing rather than reporting a component whose suite kills
		// everything: under a ceiling the baseline alone already fills, every
		// mutant dies of the bound rather than of a test.
		out.Peak, out.Bound = res.MaxRSS, maxMemory
		return finish(KindBaselineTooLarge, nil)
	}

	report, err := coverage.Measure(ctx, in.Dir, c.Dir, t.inv.CoverageReport, t.lang, in.Shape.Env(tc, c, runner.Invocation{}))
	if err != nil {
		return finish(KindMeasureFailed, err)
	}
	mutants, unmatched, err := generate(in.Dir, c, t.lang, report.Executed, t.scoped, in.Diagnostics)
	if err != nil {
		return finish(KindGenerateFailed, err)
	}
	// Before the empty case, because a declaration covering no mutant is most
	// likely exactly where nothing was generated at all.
	out.Summary.Unmatched = unmatched
	if len(mutants) == 0 {
		// Nothing to mutate is its own outcome and never a pass: a gate that
		// examined nothing must not read as one that examined everything.
		return finish(KindNothingToMutate, nil)
	}

	timeout, workers := budget(baseline, in.Timeout), workersFor(p.Ports, in.Limit)
	// Into the live stream, where the per-mutant lines go: a run too large to
	// finish is killed by its job timeout and writes no document at all, so a
	// projection only a document carried is one the reader who needs it never
	// sees. ADR 0027 refuses a runtime budget, and this is not one — nothing
	// here stops a run.
	_, _ = fmt.Fprintln(p.Log, costProjection(len(mutants), workers, timeout))

	results, err := mutation.Execute(ctx, t.backend, mutants, mutation.Options{
		Env:       in.Shape.Env(tc, c, t.suite),
		Timeout:   timeout,
		MaxMemory: maxMemory,
		Workers:   workers,
		Slots:     slots,
		Log:       p.Log,
	})
	if err != nil {
		return finish(KindExecuteFailed, err)
	}
	s := mutation.Summary{Unmatched: unmatched}
	for _, r := range results {
		s.Add(r)
	}
	out.Summary, out.Results, out.Scoped = s, results, t.scoped
	out.Elapsed = time.Since(baselineStarted)
	return finish(KindCompleted, nil)
}
