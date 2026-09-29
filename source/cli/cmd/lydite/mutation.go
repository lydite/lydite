package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/flow"
	mutationflow "lydite/lydite/internal/flows/mutation"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	mutationstages "lydite/lydite/internal/stages/mutation"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// newMutationCmd asks whether each component's suite would notice if its own
// changed code were wrong.
//
// It is a peer of scan, test and review rather than a flag on `lydite test`,
// and the reason is a compilation. ADR 0016 puts every check for one component
// in one job because they share one; mutation does not — the coverage gate
// builds the instrumented variant once, and mutation builds the plain variant
// once per mutant. A flag would have made each test job serially longer by the
// most expensive thing lydite runs, inheriting a coupling whose justification
// does not apply.
//
// It waits on nothing. A mutant is meaningless against an already-red suite,
// so a run needs a passing baseline before it mutates anything, and the
// instrumented variant is both that baseline and where the executed lines come
// from. So each component's instrumented run happens here, once, and the test
// matrix's copy of it runs beside this one rather than ahead of it.
func newMutationCmd() *cobra.Command {
	var dir string
	var components []string
	var asJSON, noColor, stream, onlyAffected, declined, noGate bool
	var concurrency, baseBranch, baseSHA, memory string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:           "mutation",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Change each component's changed code and ask whether its tests notice",
		Long: `Mutate the lines this change touched, and run each component's suite against them.

Coverage measures execution. A test that calls a function and asserts nothing
scores full marks on every line it touches; a mutant is a single deliberate
change to one of those lines, and the suite is asked whether anything fails. A
change nothing notices is a survivor, and a survivor is the only outcome that
fails the gate.

Mutants come only from lines in the change against the base — the merge-base
with the base branch, or the commit --base-sha names — and only
from lines the component's own coverage reports as executed. There is no
whole-repository mode: it would run for hours on any mature codebase, and a
mutant on an uncovered line cannot be killed by construction.

An author who believes a survivor is unkillable declares it with a
` + mutationMarker + ` comment trailing the mutated line itself — the same
line, not the line above it — and one marker covers every innermost mutant on
that line. A formatter that moves a trailing comment off its line (Biome does,
on a line ending in ` + "`{`" + `) needs a ` + "`// biome-ignore format`" + ` comment above it to
keep the marker where the mutation gate reads it. The declared mutant is
generated, counted and never run — and, because internal/referral reads the
same token as a suppression, declaring one refers the change to a human.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streamDiagnostics(asJSON)
			rep := ui.NewReport("mutation")

			// Checked before anything else in this RunE, and answered with
			// no component loaded, planned, scheduled or built: a repository
			// that told CI not to run mutation this run has nothing here to
			// measure, and the row says so rather than the section going
			// missing the way an unrun job would leave it. See ADR 0044.
			if declined {
				rep.Add(ui.Row{Status: ui.StatusDeclined, Label: "mutation", Value: "declined for this run"})
				return renderReport(cmd, rep, dir, asJSON, noColor)
			}

			// The same interrupt handling `lydite test` installs, and for the
			// same reason: this command starts a component's compose stack
			// and removes it in a deferred teardown, so a signal that skipped
			// those defers would leave one stack per started component
			// holding the ports the next run has to bind.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				stop() // [lydite:exclude_from_mutation][its only effect is to restore the default disposition for the second interrupt, and a test that observed that would be a test signalling the test binary to death]
			}()

			limit, err := resolveConcurrency(concurrency)
			if err != nil {
				return err
			}
			if timeout < 0 {
				return fmt.Errorf("--timeout must not be negative, got %s", timeout)
			}
			maxMemory, err := parseBytes(memory)
			if err != nil {
				return fmt.Errorf("--memory: %w", err)
			}

			// Mutation is diff-scoped always, so the base is not something this
			// command can do without: an unresolvable one fails the run naming
			// the fix rather than mutating nothing and reporting a pass.
			// --base-sha names the commit outright and --base-branch asks where
			// this branch diverged from one; they answer the same question two
			// ways, which is why cobra refuses both at once.
			mutate, err := mutationflow.New()
			if err != nil {
				return err
			}
			r, err := mutate.Run(ctx, mutationflow.Params{
				Dir:          dir,
				Components:   components,
				Toolchains:   commandToolchains{cmd},
				BaseBranch:   baseBranch,
				BaseSHA:      baseSHA,
				OnlyAffected: onlyAffected,
				Affected:     affectedFrom,
				Shape:        mutationShape{},
				Lifecycle:    &mutationLifecycle{},
				Limit:        limit,
				Timeout:      timeout,
				Memory:       maxMemory,
				Stream:       stream,
				// The process's own stderr, where a declaration that matched
				// no mutant is named beside the per-component mirror.
				Diagnostics: os.Stderr,
			}.Inputs())
			if err != nil {
				return mutationError(err)
			}
			loaded, err := flow.Output[mutationstages.LoadDeclarationOut](r, mutationflow.StageLoadDeclaration)
			if err != nil {
				return err
			}
			if !loaded.Declared {
				rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: "mutation",
					Value: "no components declared in " + component.FileName})
				return renderReport(cmd, rep, dir, asJSON, noColor)
			}
			selection, err := flow.Output[mutationstages.SelectAffectedOut](r, mutationflow.StageSelectAffected)
			if err != nil {
				return err
			}
			ran, err := flow.Output[mutationstages.RunMutantsOut](r, mutationflow.StageRunMutants)
			if err != nil {
				return err
			}
			kept := addMutationRows(rep, selection, ran, mutationReporting{
				onlyAffected: onlyAffected,
				declared:     len(loaded.File.Components),
				limit:        limit,
				noGate:       noGate,
				// A run responsible for part of the declaration emits no
				// summary row, for the reason it emits no coverage(repo):
				// the figure counts over the whole repository, and a shard
				// holding two of four components would publish its own two
				// under a label about all of them.
				summary: len(components) == 0,
			})
			if err := recordMutants(ctx, cmd, dir, kept); err != nil {
				return err
			}
			return renderReport(cmd, rep, dir, asJSON, noColor)
		},
	}
	cmd.AddCommand(newMutationMergeCmd())
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&components, "component", nil,
		"component this run is responsible for; repeatable, and every declared component by default")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	cmd.Flags().StringVar(&concurrency, "concurrency", strconv.Itoa(defaultConcurrency),
		`how many suites to run at once, or "max" for no bound`)
	cmd.Flags().BoolVar(&onlyAffected, "affected", false,
		"of the components this run is responsible for, mutate only those the change against the base could have broken")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	// The base a caller states rather than one lydite discovers. A post-merge
	// run is where the difference is load-bearing: on the default branch the
	// merge-base against the default branch is HEAD itself, so a run there has
	// no diff and nothing to mutate.
	cmd.Flags().StringVar(&baseSHA, "base-sha", "",
		"exact commit the change is measured against, as a SHA or a relative ref such as HEAD~1; "+
			"resolved with git rev-parse and never fetched, so no merge-base is computed")
	// Two answers to one question. Picking a winner silently would let a
	// mis-wired workflow mutate a range nobody asked for.
	cmd.MarkFlagsMutuallyExclusive("base-branch", "base-sha")
	// Overrides the budget derived from the component's own baseline. A
	// number nobody measured is what ADR 0027 refuses as a runtime budget;
	// this is the escape for a suite whose own timing is not representative,
	// and it is a flag rather than a key for the reason --concurrency is.
	cmd.Flags().DurationVar(&timeout, "timeout", 0,
		"how long one mutant's suite may run before it counts as killed; derived from the component's own baseline by default")
	// The same escape as --timeout, for the other half of what bounds a
	// mutant: a suite whose own peak is not representative of what its mutants
	// need. A bound below what the component's baseline itself held leaves the
	// component unmeasured rather than reporting every mutant as killed.
	cmd.Flags().StringVar(&memory, "memory", "",
		"how much memory one mutant's suite may hold before it counts as killed, e.g. 4GiB; derived from the component's own baseline by default")
	cmd.Flags().BoolVar(&stream, "stream", false, "mirror each component's output to stderr as it runs, as well as to its log")
	cmd.Flags().BoolVar(&declined, "declined", false,
		"write a report saying this repository declined mutation testing for this run, and do nothing else")
	// The post-merge run, where a survivor is history rather than a verdict:
	// the change has landed, the branch is gone, and the remedy a failing row
	// prints is addressed to a pull request that no longer exists. Only a
	// completed measurement stops voting — a variant that is not runnable, an
	// unresolvable base and every error still fail, which is the family a
	// `continue-on-error` on the step could not tell a survivor from. Spelled
	// as the negation of a default because mutation gates by default, the way
	// `lydite test`'s --no-coverage is. See ADR 0048.
	cmd.Flags().BoolVar(&noGate, "no-gate", false,
		"measure and record every mutant as usual, and let no survivor fail the command")
	return cmd
}

// annotationMarker is the declaration an author writes to say no test could
// kill a mutant, in the form the help and a failing row quote it: the comment
// introducer, the token, and where the reason goes. One statement of it, so the
// command's own words cannot drift from what internal/annotation reads.
var annotationMarker = "// " + annotation.Marker(annotation.Mutation) + "[<reason>]"

// mutationMarker is that token as the help text quotes it.
var mutationMarker = "`" + annotationMarker + "`"

// mutationLabel is how every row about one component is named.
//
// Labelled rather than bare, for the reason a test row is: nothing forbids a
// component called `select`, `schedule` or `mutation`, and a consumer keying
// rows by label would silently lose the gate row to a component sharing its
// name.
func mutationLabel(name string) string { return "mutation(" + name + ")" }

// componentMutation is one component's outcome, kept alongside its row so the
// summary counts what the rows say.
type componentMutation struct {
	summary  mutation.Summary
	elapsed  time.Duration
	ran      bool
	findings []finding.Finding
}

// recordMutants writes what this run made of its mutants beside its report.
//
// Unconditionally, and never only under a flag: a count that reaches the
// recording step only when somebody remembered one records nothing when they
// forget. kept is the outcomes whose verdict stands, and a run that mutated no
// component writes a document naming its tree and holding no component, which
// is what a later fold needs to tell a shard that ran nothing from a shard
// whose job died.
//
// Out from under the run's cancellation, because a run cut short still reports
// the components that finished — the same rule a component's teardown runs
// under. Every failure to record warns and none of them fails the command: the
// mutants ran, their verdict is in the report, and losing the byproduct is not
// a reason to discard it. The one error returned is the record flow itself
// failing to build or start.
func recordMutants(ctx context.Context, cmd *cobra.Command, dir string, kept []mutationstages.ComponentOutcome) error {
	record, err := mutationflow.NewRecord()
	if err != nil {
		return err
	}
	_, err = record.Run(context.WithoutCancel(ctx), mutationflow.RecordParams{
		Dir:        dir,
		ReportsDir: reportsDir(dir),
		Ignore: func(dir string) error {
			ignoreReports(dir)
			return nil
		},
		Outcomes: kept,
		Warnings: cmd.ErrOrStderr(),
	}.Inputs())
	return mutationError(err)
}

// mutationError is a run's failure as this command reports it: the stage's
// own error, not the flow's framing of it.
func mutationError(err error) error {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

// mutationLogKind names this command's per-component log, which openLog writes
// as `<reports>/<component>/mutation.log`. The fold reads that file back out of
// a shard's uploaded directory, so the name is one constant rather than a
// literal at each end.
const (
	mutationLogKind = "mutation"
	mutationLogName = mutationLogKind + ".log"
)

// mutationShape answers what a component's declaration implies exactly as
// `lydite test` and the coverage gate answer it, so a mutation run can never
// derive a variant, an environment or a scope they would not.
type mutationShape struct{}

func (mutationShape) Invocation(c component.Component, v runner.Variant) (runner.Invocation, error) {
	return invocation(c, v)
}

func (mutationShape) Lang(c component.Component) runner.Lang { return langOf(c) }

func (mutationShape) Env(tc *toolchain.Env, c component.Component, inv runner.Invocation) []string {
	return childEnv(tc, c, inv)
}

func (mutationShape) NoSuite(c component.Component) bool { return declaresNoSuite(c) }

func (mutationShape) Scope(changed map[string][]int, c component.Component) map[string][]int {
	return scopeToComponent(changed, measurement{Name: c.Name, Dir: c.Dir, Lang: langOf(c)})
}

// mutationLifecycle is everything around a component's suite, done the way
// `lydite test` does it: the plan and the log, the report path, the runner's
// preparation, the services, and the setup and teardown commands.
//
// Each of those helpers decides its own failing row, and the row is what this
// command reports. It crosses the stage as a lifecycleRowError, which the
// stage hands back unread and the command unwraps into the row it already was.
//
// Plan runs once, before any other method, and is the only one that writes
// plans: every later call reads them, from whichever of the scheduler's
// goroutines is running that component.
type mutationLifecycle struct {
	plans  []componentPlan
	byName map[string]componentPlan
}

func (l *mutationLifecycle) Plan(ctx context.Context, root string, selected []component.Component, stream bool) []mutationstages.Planned {
	l.plans = planComponents(ctx, root, selected, mutationLogKind, stream)
	l.byName = make(map[string]componentPlan, len(l.plans))
	out := make([]mutationstages.Planned, len(l.plans))
	for i, p := range l.plans {
		l.byName[p.c.Name] = p
		out[i] = mutationstages.Planned{
			Component: p.c,
			Log:       p.log.out,
			LogRel:    p.log.Rel,
			Ready:     p.ready,
			Ports:     p.ports,
			Item:      itemFor(p),
		}
		if !p.ready {
			out[i].NotReady = lifecycleRowError{p.row}
		}
	}
	return out
}

func (l *mutationLifecycle) Close() {
	for _, p := range l.plans {
		p.log.Close()
	}
}

func (l *mutationLifecycle) ClearReport(dir, report string) error {
	return clearReport(dir, report)
}

func (l *mutationLifecycle) Prepare(ctx context.Context, name string, inv runner.Invocation, dir, root string, cfg config.Config, tc *toolchain.Env) error {
	p := l.byName[name]
	if row, ok := prepare(ctx, inv, dir, root, mutationLabel(name), p.c, cfg, tc, p.log); !ok {
		return lifecycleRowError{row}
	}
	return nil
}

func (l *mutationLifecycle) StartServices(ctx context.Context, name string) (func(), error) {
	stop, row, ok := startServices(ctx, l.byName[name], mutationLabel(name))
	if !ok {
		return nil, lifecycleRowError{row}
	}
	return stop, nil
}

func (l *mutationLifecycle) RunCommands(ctx context.Context, name, dir, kind string, cmds []string, tc *toolchain.Env) error {
	p := l.byName[name]
	if row, ok := runCommands(ctx, dir, mutationLabel(name), p.c, tc, kind, cmds, p.log); !ok {
		return lifecycleRowError{row}
	}
	return nil
}

// lifecycleRowError is a row a lifecycle helper already decided, carried
// through the stage as an error.
//
// Its text is the row's detail joined, which is what a worker whose
// preparation failed reports: that failure reaches the report through the
// executor's own error, never through the row, so the text is all of it that
// arrives.
type lifecycleRowError struct{ row ui.Row }

func (e lifecycleRowError) Error() string { return strings.Join(e.row.Detail, "; ") }

// lifecycleRow is the row a Lifecycle error stands for.
//
// Every error this command's own Lifecycle returns carries one. Anything else
// fails the component with the error as its reason, since a failure nobody
// decided a row for is still one the component did not get past.
func lifecycleRow(label string, err error) ui.Row {
	var decided lifecycleRowError
	if errors.As(err, &decided) {
		return decided.row
	}
	return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}
}

// mutationReporting is what the command decided about how a run's outcomes
// are reported.
type mutationReporting struct {
	// onlyAffected is whether selection ran, and declared how many components
	// the whole declaration names — what the select row counts against.
	onlyAffected bool
	declared     int
	limit        int
	// noGate is whether a completed component's outcome stops voting on the
	// exit code. True under --no-gate, where the mutants still run, the
	// findings are still emitted and mutants.json is still written. The zero
	// value keeps gating, so a caller that leaves this unset gets a gating
	// run rather than a silent, unasked-for --no-gate.
	noGate  bool
	summary bool
}

// addMutationRows adds what a run made of every component to rep, and returns
// the outcomes whose verdict stands.
//
// The select row first, when selection ran; then the schedule; then one row
// per component in declaration order, a skipped one interleaved where its
// author wrote it; then every survivor's finding in plan order, so two runs of
// one declaration hand the same document to whatever anchors them; then the
// summary.
//
// An interrupted run withdraws every failing verdict a scheduled component
// reached, findings and all: under cancellation a survivor cannot be told from
// a mutant whose suite was killed, and a claim nobody can stand behind is worse
// than none. A withdrawn component is absent from what is returned, exactly as
// one that never ran is.
//
// What is withdrawn is decided by the row a gating run renders, whatever
// --no-gate says: the flag turns a survivor's fail into context, and a
// withdrawal keyed on the displayed row would keep claims under --no-gate that
// the same interrupted run takes back when it gates. A component the gating row
// keeps renders its own row, so the vote is the one thing --no-gate changes.
func addMutationRows(rep *ui.Report, sel mutationstages.SelectAffectedOut, run mutationstages.RunMutantsOut, how mutationReporting) []mutationstages.ComponentOutcome {
	var skipped map[string]ui.Row
	if how.onlyAffected {
		rep.Add(selectRow(sel.Selection, how.declared))
		skipped = make(map[string]ui.Row, len(sel.Skipped))
		for _, c := range sel.Skipped {
			skipped[c.Name] = ui.Row{Status: ui.StatusUnmeasured,
				Label: mutationLabel(c.Name), Value: "not affected"}
		}
	}

	rows := make([]ui.Row, len(run.Components))
	results := make([]componentMutation, len(run.Components))
	var scheduled []int
	for i, o := range run.Components {
		rows[i], results[i] = outcomeRow(o, how.noGate)
		if o.Scheduled {
			scheduled = append(scheduled, i)
		}
	}
	if run.Interrupted {
		gating := make([]ui.Row, len(run.Components))
		for i, o := range run.Components {
			gating[i], _ = outcomeRow(o, false)
		}
		withdrawInterrupted(gating, results, scheduled)
		// A scheduled component left holding no result was either withdrawn,
		// and takes the withdrawn row, or never completed, where --no-gate
		// changes nothing and the gating row is the row it already had.
		for _, i := range scheduled {
			if !results[i].ran {
				rows[i] = gating[i]
			}
		}
	}

	// Counted over the components that declare a suite: one that declares none
	// was never the scheduler's to start, so it is neither a component the
	// run started nor one it failed to.
	rep.Add(scheduleRow(endedContext(run.Interrupted), run.Schedule, run.Suites, how.limit))
	addRows(rep, rows, sel.Ordered, skipped, mutationLabel)
	for _, r := range results {
		rep.AddFindings(r.findings...)
	}
	if how.summary {
		rep.Add(mutationSummaryRow(results))
	}

	// Outcomes and results are indexed in parallel, and `ran` is set only where
	// a component's mutants were generated and executed to a summary — and not
	// where withdrawInterrupted took the run back, which resets the result and
	// with it that flag.
	var kept []mutationstages.ComponentOutcome
	for i, o := range run.Components {
		if results[i].ran {
			kept = append(kept, o)
		}
	}
	return kept
}

// endedContext is a context whose Err says whether the run was interrupted, as
// RunMutants observed it at its end. scheduleRow reads an interrupt off a
// context, and the schedule row and the withdrawal then answer from the same
// observation rather than from two readings of a context that can be cancelled
// between them.
func endedContext(interrupted bool) context.Context {
	ctx := context.Background()
	if !interrupted {
		return ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	return ctx
}

// outcomeRow is one component's row, and what the summary and the findings
// count of it, decided by what became of it.
//
// A teardown that failed takes over the row where teardownFailureReplaces says
// it does, and leaves the counts and findings beside it alone: the mutants
// said what they had to say whatever the teardown did afterwards.
func outcomeRow(o mutationstages.ComponentOutcome, noGate bool) (ui.Row, componentMutation) {
	row, out := kindRow(o, noGate)
	if o.TeardownErr != nil && teardownFailureReplaces(row.Status) {
		row = lifecycleRow(row.Label, o.TeardownErr)
	}
	return row, out
}

// kindRow is the row each OutcomeKind is reported as.
//
// Every way a component could not be mutated is a row rather than an error,
// and none of them passes: a gate that could not run never renders as one that
// did.
func kindRow(o mutationstages.ComponentOutcome, noGate bool) (ui.Row, componentMutation) {
	c := o.Component
	label := mutationLabel(c.Name)
	log := &componentLog{Rel: o.LogRel}
	switch o.Kind {
	case mutationstages.KindNotRun:
		return ui.Row{
			Status: ui.StatusUnmeasured,
			Label:  label,
			Value:  "not run",
			Detail: []string{"the run ended before this component started"},
		}, componentMutation{}
	case mutationstages.KindBlocked:
		// The one kind whose error is a row the Lifecycle already decided.
		return lifecycleRow(label, o.Err), componentMutation{}
	case mutationstages.KindMutationOff:
		// Present, so ADR 0026's completeness rule holds with no exception
		// and the fold needs no second copy of the opt-out rule to know which
		// absences are legitimate. Context and not unmeasured: the amber tag
		// is for a gate that could not run, and spending it on a decision the
		// repository stated deliberately is what teaches a reader to skim
		// past it.
		return ui.Row{Status: ui.StatusContext, Label: label, Value: "mutation is off for this component",
			Detail: []string{"`mutation: false` in " + component.FileName}}, componentMutation{}
	case mutationstages.KindRawCommand:
		return unmeasuredRow(label, "the component declares a raw command, so lydite cannot derive the build-only and plain variants a mutant needs"), componentMutation{}
	case mutationstages.KindInvocationFailed:
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{o.Err.Error()}}, componentMutation{}
	case mutationstages.KindUntouched:
		return unmeasuredRow(label, "this change touches no source this component is written in"), componentMutation{}
	case mutationstages.KindNoCoverageReport:
		return unmeasuredRow(label,
			"the runner's instrumented variant names no coverage report, so there are no executed lines to mutate"), componentMutation{}
	case mutationstages.KindClearReportFailed:
		return failure(label, log, o.Err.Error(), "not runnable", ""), componentMutation{}
	case mutationstages.KindBaselineInterrupted:
		return unmeasuredRow(label, "the run was interrupted before this component's baseline suite ran"), componentMutation{}
	case mutationstages.KindBaselineFailed:
		// Unmeasured rather than failed: nothing can be concluded about tests
		// that were not passing before the mutation, and failing here would
		// report one broken suite as two red gates whose second names a cause
		// its author clears by fixing the first.
		return detailed(unmeasuredRow(label,
			"the baseline suite did not pass, so nothing can be concluded about what a mutant would change"),
			log, tail(o.Output)...), componentMutation{}
	case mutationstages.KindBaselineTooLarge:
		return detailed(unmeasuredRow(label, fmt.Sprintf(
			"the baseline suite held %d byte(s), which leaves no room under the %d byte memory bound its mutants would run at",
			o.Peak, o.Bound)), log), componentMutation{}
	case mutationstages.KindNoBackend, mutationstages.KindMeasureFailed,
		mutationstages.KindGenerateFailed, mutationstages.KindExecuteFailed:
		// The error's own text, never unwrapped: an executor's error that
		// carries a worker's failed preparation already says everything the
		// row has to.
		return unmeasuredRow(label, o.Err.Error()), componentMutation{}
	case mutationstages.KindNothingToMutate:
		// A run with nothing to mutate must not report a pass: a green row
		// from a gate that examined nothing is indistinguishable from one
		// that examined everything.
		row := unmeasuredRow(label, "no line this change touched is both mutable and reported as executed")
		if n := unmatchedNote(o.Summary); n != "" {
			row.Detail = append(row.Detail, n)
		}
		return detailed(row, log), componentMutation{}
	case mutationstages.KindCompleted:
		out := componentMutation{summary: o.Summary, elapsed: o.Elapsed, ran: true}
		row, findings := mutationRow(label, c.Name, c.Dir, log, o.Summary, o.Results, o.Scoped, o.Elapsed)
		out.findings = findings
		return completedRow(row, noGate), out
	default:
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable",
			Detail: []string{fmt.Sprintf("lydite has no account of a %s outcome", o.Kind)}}, componentMutation{}
	}
}

// withdrawInterrupted takes back every failing verdict a cancelled run reached.
//
// The same rule runComponents applies, for the same reason: under cancellation
// lydite cannot tell a suite that failed from one that was killed, and a red
// row blaming a CI job timeout on the repository's tests is the worst available
// answer.
//
// The component's findings go with the row. A survivor is a claim that the
// suite passed with the code changed that way, and a suite that was killed
// established no such thing — so a claim left behind here would be one nobody
// can stand behind, anchored to a line, on a pull request.
func withdrawInterrupted(rows []ui.Row, results []componentMutation, index []int) {
	for _, i := range index {
		if rows[i].Status != ui.StatusFail {
			continue
		}
		rows[i] = ui.Row{
			Status: ui.StatusUnmeasured,
			Label:  rows[i].Label,
			Value:  "not completed",
			Detail: []string{"the run was interrupted before this component finished"},
			Log:    rows[i].Log,
		}
		results[i] = componentMutation{}
	}
}

// addRows adds one row per component in declaration order, interleaving the
// ones selection skipped so a component sits where its author wrote it.
func addRows(rep *ui.Report, rows []ui.Row, ordered []component.Component, skipped map[string]ui.Row, label func(string) string) {
	if len(skipped) == 0 {
		for _, r := range rows {
			rep.Add(r)
		}
		return
	}
	byLabel := make(map[string]ui.Row, len(rows))
	for _, r := range rows {
		byLabel[r.Label] = r
	}
	for _, c := range ordered {
		if r, ok := skipped[c.Name]; ok {
			rep.Add(r)
			continue
		}
		if r, ok := byLabel[label(c.Name)]; ok {
			rep.Add(r)
		}
	}
}

// completedRow is what a component whose mutants ran is worth to the exit code.
//
// Under --no-gate it is worth nothing, and the row says so as StatusContext —
// whether or not it had survivors. A component that killed every mutant renders
// the same glyph as one that did not, because a ✓ is the claim that a gate
// examined this component and cleared it, and no gate examined anything. The
// value and the detail are untouched: the numbers and the surviving mutants are
// the whole point of the run.
//
// Only a pass and a survivor are converted. A row that reached here already
// non-voting — a denominator of zero — was never a measurement this flag has
// anything to say about, and every way a component could not run at all is a
// row mutateComponent returned before this.
func completedRow(row ui.Row, noGate bool) ui.Row {
	if !noGate {
		return row
	}
	if row.Status == ui.StatusPass || row.Status == ui.StatusFail {
		row.Status = ui.StatusContext
	}
	return row
}

// teardownFailureReplaces says whether a component's teardown failing takes
// over its row.
//
// It does where the row is the component's own completed measurement: the
// mutants said what they had to say, and a stack the teardown left running is
// then the only thing left to report. StatusContext is that same measurement
// under --no-gate, and a teardown failure is a command that did not run rather
// than a measurement, so the flag does not excuse it.
//
// A row that already names why the component could not be measured keeps that
// reason, and so does a survivor a gating run is failing on: both are upstream
// of the teardown, and the cause a reader acts on is the one they name.
func teardownFailureReplaces(status ui.Status) bool {
	return status == ui.StatusPass || status == ui.StatusContext
}

// mutationRow is one component's verdict.
//
// A survivor is the only outcome that fails it. It is a gate rather than a
// referral because the author clears it by writing the assertion that kills
// the mutant, which is work that improves the code — and CONTEXT.md uses this
// exact case to say what a gate is.
//
// A component with a denominator of zero is unmeasured rather than a pass:
// every mutant was unviable or acknowledged, so nothing was established about
// the suite, and a green row from a gate that examined nothing is
// indistinguishable from one that examined everything.
func mutationRow(label, component, dir string, log *componentLog, s mutation.Summary, results []mutation.Result, changed map[string][]int, elapsed time.Duration) (ui.Row, []finding.Finding) {
	killed, total := s.Score()
	if total == 0 {
		row := unmeasuredRow(label, fmt.Sprintf(
			"%d mutant(s), none of which says anything about the suite: %s", s.Total(), aside(s)))
		if n := unmatchedNote(s); n != "" {
			row.Detail = append(row.Detail, n)
		}
		if n := unboundedNote(results); n != "" {
			row.Detail = append(row.Detail, n)
		}
		if n := heldOpenNote(results); n != "" {
			row.Detail = append(row.Detail, n)
		}
		return detailed(row, log), nil
	}
	// The elapsed time is in the value rather than under the row, because a
	// reader deciding whether this component is worth mutating on every pull
	// request reads it beside the score it bought. It is prose for that reader
	// alone: mutants.json carries the same span as a number, and the fold and
	// the ledger both take it from there rather than from this sentence.
	row := ui.Row{Status: ui.StatusPass, Label: label, Log: log.Rel,
		Value: fmt.Sprintf("%d of %d mutant(s) killed in %s", killed, total, elapsed.Round(time.Second))}
	if a := aside(s); a != "" {
		row.Detail = append(row.Detail, a)
	}
	if n := unmatchedNote(s); n != "" {
		row.Detail = append(row.Detail, n)
	}
	if n := unboundedNote(results); n != "" {
		row.Detail = append(row.Detail, n)
	}
	if n := heldOpenNote(results); n != "" {
		row.Detail = append(row.Detail, n)
	}
	survivors := mutation.Survivors(results)
	if len(survivors) == 0 {
		return row, nil
	}
	row.Status = ui.StatusFail
	row.Value = fmt.Sprintf("%d of %d mutant(s) survived in %s", len(survivors), total, elapsed.Round(time.Second))
	findings := mutationFindings(label, component, dir, survivors, changed)
	// Every survivor, not a sample: the author's next action is to write an
	// assertion for each one, and a truncated list makes that a second run to
	// discover the rest.
	row.Detail = nil
	for _, f := range findings {
		row.Detail = append(row.Detail, f.Message)
	}
	if a := aside(s); a != "" {
		row.Detail = append(row.Detail, a)
	}
	if n := unmatchedNote(s); n != "" {
		row.Detail = append(row.Detail, n)
	}
	if n := unboundedNote(results); n != "" {
		row.Detail = append(row.Detail, n)
	}
	if n := heldOpenNote(results); n != "" {
		row.Detail = append(row.Detail, n)
	}
	row.Detail = append(row.Detail,
		"write the assertion that fails when the code changes this way, or declare the mutant equivalent with "+
			annotationMarker+" trailing the mutated line — one marker there covers every innermost mutant on it")
	if log.Rel != "" {
		row.Detail = append(row.Detail, "full output: "+log.Rel)
	}
	return row, findings
}

// mutationFindings is the claim a failing mutation row makes, one per
// survivor.
//
// A mutant that was killed is evidence the suite works and no claim about the
// code, so only survivors become findings — the rule every gate here follows,
// that findings are emitted exactly where a claim is made the author must
// clear. An acknowledged mutant is not among them either: a human has already
// read that claim and answered it in the source.
//
// The site is what the mutant did rather than where it did it: the operator,
// the text replaced and the text replacing it. Two identical comparisons in
// one file produce two mutants alike in all of that, which is what the ordinal
// separates.
//
// Paths are made relative to the scan root, because a mutant's own path is
// relative to the component it came from and a claim that names one file from
// two roots is two claims.
func mutationFindings(label, component, dir string, survivors []mutation.Result, changed map[string][]int) []finding.Finding {
	out := make([]finding.Finding, 0, len(survivors))
	for _, r := range survivors {
		m := r.Mutant
		out = append(out, finding.Finding{
			Gate:      "mutation",
			Component: component,
			Row:       label,
			Path:      path.Join(dir, m.Path),
			Line:      m.Line,
			Message:   m.String(),
			Detail: []string{
				"This mutant survived: the suite passed with the code changed this way.",
				"Write the assertion that fails when it is, or declare the mutant equivalent with " +
					annotationMarker + " trailing the mutated line — one marker there covers every innermost mutant on it.",
			},
			Site: string(m.Operator) + "\x1f" + finding.Normalise(m.Original) + "\x1f" + finding.Normalise(m.Mutated),
		})
	}
	finding.Number(out)
	// A mutant exists only on a line the change touched, so this establishes
	// what generation already guaranteed. It is asked anyway, so the rule
	// deciding how a claim reaches a change has one implementation.
	finding.Anchored(out, changed)
	return out
}

// aside names the mutants that are in the run and not in the denominator.
//
// Both are excluded because neither is evidence about the tests — one could not
// be built and the other has been declared unkillable — and both are worth
// seeing: a component whose mutants are mostly unviable is telling its reader
// about the generator, and one whose survivors are mostly acknowledged is
// telling them about the claims a human has been asked to read.
func aside(s mutation.Summary) string {
	var parts []string
	if s.Unviable > 0 {
		parts = append(parts, fmt.Sprintf("%d did not compile", s.Unviable))
	}
	if s.Acknowledged > 0 {
		parts = append(parts, fmt.Sprintf("%d declared equivalent (refers this change for review)", s.Acknowledged))
	}
	if s.TimedOut > 0 {
		parts = append(parts, fmt.Sprintf("%d timed out", s.TimedOut))
	}
	if s.OutOfMemory > 0 {
		parts = append(parts, fmt.Sprintf("%d ran out of memory", s.OutOfMemory))
	}
	return strings.Join(parts, ", ")
}

// unmatchedNote counts the equivalence declarations that covered no mutant.
//
// A line of its own rather than a part of aside, because these are not mutants
// at all: aside's parts are each a share of the mutants the run generated, and
// a declaration covering nothing adds no mutant to any of them. It is on the
// row, and not only on the diagnostic stream, because the row is what a
// reader of the report or of the pull-request comment sees — and the author of
// such a declaration believes they have answered a survivor that is generated
// and run anyway. It gates nothing.
func unmatchedNote(s mutation.Summary) string {
	if s.Unmatched == 0 {
		return ""
	}
	return fmt.Sprintf("%d declaration(s) cover no mutant", s.Unmatched)
}

// unboundedNote says on the row that the memory bound did not reach the
// mutants it was asked for.
//
// Darwin is where that happens: setrlimit refuses RLIMIT_DATA and RLIMIT_AS
// alike with EINVAL, at any value, so a mutant there runs bounded in time and
// unbounded in memory. Said out loud rather than left out, because a bound
// quietly not applied renders exactly the green of one that held.
func unboundedNote(results []mutation.Result) string {
	if !mutation.Unbounded(results) {
		return ""
	}
	return "memory was not bounded: this platform has no limit to set, so a mutant that allocates without stopping was held to nothing"
}

// heldOpenNote says on the row that a mutant's own process group had to be
// killed at the wait delay because something it started was still holding
// its output open when the suite itself had already exited.
//
// Said out loud rather than left out, because a mutant reporting killed or
// survived this way exited clean only because lydite stopped waiting for
// what it left running, not because nothing was left running.
func heldOpenNote(results []mutation.Result) string {
	if !mutation.HeldOutputOpen(results) {
		return ""
	}
	return "a mutant exited while something it started was still running: its process group was killed after lydite stopped waiting for it"
}

// parseBytes reads a byte quantity as a flag spells one: a plain number of
// bytes, or one with a binary suffix.
//
// Binary rather than decimal for every suffix, including the bare K, M and G:
// the quantity is compared against a peak the kernel reports in pages, and a
// tool whose GB and GiB were a 7% different ceiling would be one nobody could
// reason about from the row.
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	digits, shift := s, 0
	for i, suffix := range []string{"K", "M", "G"} {
		for _, spelling := range []string{suffix, suffix + "B", suffix + "iB"} {
			if cut, ok := strings.CutSuffix(s, spelling); ok {
				digits, shift = strings.TrimSpace(cut), 10*(i+1)
			}
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a byte quantity — write bytes, or a size like 4GiB: %w", s, err)
	}
	// A negative ceiling bounds nothing and a suffix that overflowed reads as
	// one, so both are refused where the number is read rather than where a
	// suite would fail under it.
	scaled := n << shift
	if n < 0 || scaled>>shift != n {
		return 0, fmt.Errorf("%q is not a memory bound a suite could run under", s)
	}
	return scaled, nil
}

// mutationSummaryRow counts the run, and gates nothing.
//
// There is no repository-wide figure only a fold can compute: survived == 0
// for every component is survived == 0 for the repository, so a gating row
// here could only restate the conjunction of the rows above it. What it
// carries is the counts and the elapsed time, which is what makes a runtime
// budget a measured decision later rather than an invented number now.
func mutationSummaryRow(results []componentMutation) ui.Row {
	var total mutation.Summary
	var elapsed time.Duration
	ran := 0
	for _, r := range results {
		if !r.ran {
			continue
		}
		ran++
		elapsed += r.elapsed
		total.Killed += r.summary.Killed
		total.TimedOut += r.summary.TimedOut
		total.OutOfMemory += r.summary.OutOfMemory
		total.Survived += r.summary.Survived
		total.Unviable += r.summary.Unviable
		total.Acknowledged += r.summary.Acknowledged
	}
	if ran == 0 {
		return ui.Row{Status: ui.StatusContext, Label: "mutation", Value: "no component was mutated"}
	}
	killed, denom := total.Score()
	row := ui.Row{Status: ui.StatusContext, Label: "mutation",
		Value: fmt.Sprintf("%d of %d mutant(s) killed across %d component(s) in %s",
			killed, denom, ran, elapsed.Round(time.Second))}
	if a := aside(total); a != "" {
		row.Detail = []string{a}
	}
	return row
}

// detailed hangs the tail of what a component printed under its row, and names
// the log holding the rest.
//
// The same three things a failing suite row carries, for the same reason: a
// reader looking at an amber row should not have to scroll past another
// component's container lifecycle to learn why, and the log is what survives
// when the tail is not enough.
func detailed(row ui.Row, log *componentLog, lines ...string) ui.Row {
	row.Detail = append(row.Detail, lines...)
	if log.Rel != "" {
		row.Detail = append(row.Detail, "full output: "+log.Rel)
		row.Log = log.Rel
	}
	return row
}
