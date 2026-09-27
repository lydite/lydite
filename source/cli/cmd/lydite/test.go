package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/flow"
	testflow "lydite/lydite/internal/flows/test"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	teststages "lydite/lydite/internal/stages/test"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

func newTestCmd() *cobra.Command {
	var dir string
	var components []string
	var asJSON, noColor, stream, onlyAffected, noCoverage, gateCoverage, gateFlaky bool
	var concurrency, baseBranch string
	cmd := &cobra.Command{
		Use:           "test",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Run each declared component's test suite",
		Long: `Run the test suite of every component declared in ` + component.FileName + `.

lydite invokes each component's runner in the component's own directory; it
never learns to run anyone's tests. What runs, and with which arguments, is
the component's to declare.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streamDiagnostics(asJSON)
			rep := ui.NewReport("test")

			// An interrupt cancels this command's context rather than killing
			// the process where it stands. `lydite test` starts containers and
			// removes them in a deferred teardown, and a signal that skips
			// those defers leaves one stack running per component that had
			// started — holding the host ports the next run has to bind, so
			// the leak surfaces as an unrelated failure one run later.
			//
			// Scoped to this command and not installed in main, because it is
			// the only one that reports a cancelled run honestly. `scan` and
			// `coverage` would render every killed tool as a finding, and
			// publish a complete-looking document saying every scanner failed
			// — a check that could not run reading as one that did, which is
			// worse than the process dying.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// Unregister as soon as the first signal lands, so the second one
			// gets the default disposition and kills the process. The handler
			// otherwise stays installed with nothing reading its channel, and
			// a teardown hanging on an unresponsive container daemon could not
			// be interrupted at all.
			go func() {
				<-ctx.Done()
				stop()
			}()

			// Before any work: a typo must not pay for a git walk first, and
			// must not discard a report the run had already computed.
			limit, err := resolveConcurrency(concurrency)
			if err != nil {
				return err
			}
			// Here rather than beside the selection below, for the reason
			// above: the gates each walk the whole tree, and a flag conflict
			// must not pay for that first and then discard a report the run
			// had already computed.
			if noCoverage && gateCoverage {
				// Gating what was never measured has no answer to give, and
				// silently ignoring one of the two flags would leave a
				// workflow believing it gates.
				return errors.New("--no-coverage measures nothing for --gate-coverage to gate; pass one")
			}
			tests, err := testflow.New()
			if err != nil {
				return err
			}
			r, err := tests.Run(ctx, testflow.Params{
				Dir:          dir,
				Components:   components,
				BaseBranch:   baseBranch,
				Affected:     onlyAffected,
				GateFlaky:    gateFlaky,
				GateCoverage: gateCoverage,
				Concurrency:  limit,
				Stream:       stream,
				Instrument:   !noCoverage,
				Logs:         componentLogs,
				Stderr:       cmd.ErrOrStderr(),
			}.Inputs())
			if err != nil && !interrupted(err, r) {
				return testError(err)
			}
			if err := addTestRows(cmd, rep, dir, r); err != nil {
				return err
			}
			// A run cut short after its suites reports what they did through
			// the schedule row, which fails. One that reports nothing failing
			// was cut short after that row was written, and rendering it would
			// have the gates it never reached read as a green run.
			if err != nil && rep.Err() == nil {
				return testError(err)
			}
			return renderReport(cmd, rep, dir, asJSON, noColor)
		},
	}
	// A subcommand beside a runnable command: cobra resolves subcommands
	// before positional arguments, so `lydite test` still runs the suites and
	// `lydite test record` lands what they measured.
	cmd.AddCommand(newRecordCmd(), newPlanCmd(), newMergeCmd())
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	// What this run is responsible for. It reports one row per component
	// named here and nothing at all about any other, which is what lets
	// `lydite test merge` fold a matrix of shards into one document — and
	// what `--affected` then narrows within.
	cmd.Flags().StringSliceVar(&components, "component", nil, "component this run is responsible for; repeatable, and every declared component by default")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	// A flag and never a key in .lydite/config.yml: how many components a
	// machine can run at once is a fact about the machine, and a four-core
	// runner reading a number committed from a thirty-two-core workstation is
	// exactly the drift a flag avoids.
	cmd.Flags().StringVar(&concurrency, "concurrency", strconv.Itoa(defaultConcurrency),
		`how many components to run at once, or "max" for one slot per selected component`)
	// Off by default, and never inferred. Every signal for "is this a pull
	// request" is unreliable where lydite runs — a detached HEAD, a shallow
	// clone, a fork with no upstream fetched, a default branch not called
	// main — and guessing wrong in the narrowing direction is the one failure
	// selection cannot report. The caller already knows the event, so it says
	// so; a bare `lydite test` always runs every component.
	cmd.Flags().BoolVar(&onlyAffected, "affected", false,
		"of the components this run is responsible for, run only those the change against the merge-base could have broken")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	// Instrumentation is on by default despite costing real time — cargo
	// llvm-cov forces a separate instrumented build, and Go's -coverpkg=./...
	// recompiles every package per test binary. The fast inner loop is `go
	// test` or `cargo nextest` run directly; nothing reaches for lydite to
	// re-run one package. A gate that is opt-in is a gate that is off where
	// it matters.
	cmd.Flags().BoolVar(&noCoverage, "no-coverage", false,
		"run each component's plain variant and report no coverage at all")
	// Explicit, the way --affected is. Measuring is local; gating fetches the
	// baseline branch and pushes to it, and a developer's local run must
	// never write to a shared branch. Inferring "am I in CI" is what ADR 0018
	// already refused for selection, for the same reason.
	cmd.Flags().BoolVar(&gateCoverage, "gate-coverage", false,
		"compare each component's coverage against the baseline for the merge-base, and record this tree's")
	// Explicit, the sibling of --gate-coverage, for the reason ADR 0018 gave
	// for selection: a gate inferred from "am I in CI" guesses, and the caller
	// already knows the event. Asking for it makes a Go component's suite write
	// a JUnit report whichever variant it ran, since the first of the two
	// outcomes is read from the report the run already wrote.
	cmd.Flags().BoolVar(&gateFlaky, "gate-flaky", false,
		"rerun each Go component's new tests once, in a process of their own, and fail on a test whose two outcomes disagree")
	// Every run captures; this only adds the terminal. A suite that hangs
	// prints nothing until it is killed, and its log is written but not yet
	// interesting — watching it is the case a captured file cannot serve.
	cmd.Flags().BoolVar(&stream, "stream", false, "mirror each component's output to stderr as it runs, as well as to its log")
	return cmd
}

// testLabel is testrun.TestLabel.
func testLabel(name string) string { return testrun.TestLabel(name) }

// declaresNoSuite is testrun.DeclaresNoSuite.
func declaresNoSuite(c component.Component) bool { return testrun.DeclaresNoSuite(c) }

// noSuiteReason is testrun.NoSuiteReason.
const noSuiteReason = testrun.NoSuiteReason

// noSuiteTestRow is testrun.NoSuiteTestRow.
func noSuiteTestRow(kind, name string) ui.Row { return testrun.NoSuiteTestRow(kind, name) }

// noSuiteFlakyRow is testrun.NoSuiteFlakyRow.
func noSuiteFlakyRow(name string) ui.Row { return testrun.NoSuiteFlakyRow(name) }

// noSuiteCoverageRows adds testrun.NoSuiteCoverageRows to rep, in the order it
// returns them.
func noSuiteCoverageRows(rep *ui.Report, name string, floor float64) {
	for _, row := range testrun.NoSuiteCoverageRows(name, floor) {
		rep.Add(row)
	}
}

// testError is a run's failure as this command reports it: the stage's own
// error, never the flow's framing of it. A configuration that will not load or
// a component nobody declared reads the same whichever stage found it.
func testError(err error) error {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

// interrupted reports whether err is the flow stopping for a cancelled context
// once the run stage had finished, rather than a stage failing.
//
// The suites are the part of a run that takes long enough to be interrupted,
// and the run stage turns an interruption into rows of its own — a failing
// schedule row, and each unfinished component not completed — so the report
// those rows belong to is still the answer to give. A cancellation any stage
// returned as its own error is that stage failing, and one before the run
// stage finished has no suite's rows to report.
func interrupted(err error, r *flow.Result) bool {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return false
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return r.Status(testflow.StageRun) == flow.StatusSucceeded
}

// addTestRows adds every section of the run to rep, in the order the flow ran
// the stages that produced them: the declaration's gates, selection, the
// suites, the flaky gate, and coverage.
//
// A stage after the run that an interruption stopped the flow before adds
// nothing: the schedule row already says the run was cut short.
func addTestRows(cmd *cobra.Command, rep *ui.Report, root string, r *flow.Result) error {
	declared, err := flow.Output[teststages.DeclarationOut](r, testflow.StageDeclaration)
	if err != nil {
		return err
	}
	selected, err := flow.Output[teststages.SelectAffectedOut](r, testflow.StageSelectAffected)
	if err != nil {
		return err
	}
	ran, err := flow.Output[teststages.RunOut](r, testflow.StageRun)
	if err != nil {
		return err
	}
	flaky, err := reachedOutput[teststages.FlakyGateOut](r, testflow.StageFlakyGate)
	if err != nil {
		return err
	}
	measured, err := reachedOutput[teststages.CoverageOut](r, testflow.StageCoverage)
	if err != nil {
		return err
	}
	for _, rows := range [][]ui.Row{declared.Rows, selected.Rows, ran.Rows, flaky.Rows} {
		for _, row := range rows {
			rep.Add(row)
		}
	}
	rep.AddFindings(flaky.Findings...)
	addCoverage(cmd, rep, root, measured)
	return nil
}

// reachedOutput is flow.Output for a stage the flow may have stopped before,
// which is T's zero value. A stage that was reached and has no output is still
// the error flow.Output makes it.
func reachedOutput[T any](r *flow.Result, stage string) (T, error) {
	if r.Status(stage) == flow.StatusNotReached {
		var zero T
		return zero, nil
	}
	return flow.Output[T](r, stage)
}

// addCoverage adds the coverage section to rep: every coverage, patch, CRAP and
// floor row, then the record row once the candidate the run proposes is saved,
// then the rows of each component declaring no suite.
func addCoverage(cmd *cobra.Command, rep *ui.Report, root string, out teststages.CoverageOut) {
	for _, row := range out.Rows {
		rep.Add(row)
	}
	// `record` and not `baseline`: the two are different events in one run —
	// the baseline read this change is gated against, and the entry this
	// change leaves for the next one. Sharing a label would put two rows under
	// it, which is what a consumer keying rows by label cannot survive.
	if c := out.Candidate; c != nil {
		rep.Add(candidateRow(cmd, root, candidateDoc(*c), c.Value))
	}
	for _, row := range out.NoSuiteRows {
		rep.Add(row)
	}
}

// candidateDoc is the document a candidate is saved as. One that establishes
// nothing names only its tree, why, and the test counts, which a run whose
// every suite failed still has and a history most wants.
func candidateDoc(c teststages.Candidate) measurementsDoc {
	if c.Reason != "" {
		return measurementsDoc{Tree: c.Tree, Reason: c.Reason, Tests: c.Tests}
	}
	return measurementsFrom(c.Tree, c.Record, c.Measured, c.Carried, c.Scores, c.GatedAgainst, c.Gated, c.Parts, c.Tests)
}

// defaultConcurrency is how many components run at once when nothing says
// otherwise.
//
// Deliberately a constant rather than something derived from NumCPU. Every
// runner lydite drives is already internally parallel — go test fans out at
// GOMAXPROCS, cargo nextest runs tests concurrently, vitest forks workers — so
// one component already tries to use the whole machine and NumCPU of them
// oversubscribe it quadratically. The symptom is timing-sensitive tests going
// flaky, which reads as a bad suite rather than as a bad bound.
//
// It is not 1. A scheduler that never runs two components at once passes every
// assertion about port locks without once having taken one, so a bound of 1 by
// default would leave the constraint unexercised everywhere it is not asked
// for explicitly.
const defaultConcurrency = 4

// resolveConcurrency turns the flag into a slot count.
//
// "max" is a slot for every component, for a caller that wants a shard to run
// everything it was given at once; the scheduler never admits more than it has,
// so the bound needs no count to be resolved against and can be checked before
// any work happens. It bounds the components inside one process and says
// nothing about how many jobs a matrix has — `lydite test plan` takes no such
// knob, because one word meaning both is one a reader cannot tell apart.
//
// A number below one is refused rather than clamped: it is a typo, and
// silently running anyway would have lydite ignore something the caller said.
func resolveConcurrency(flag string) (int, error) {
	if flag == "max" {
		return math.MaxInt, nil
	}
	n, err := strconv.Atoi(flag)
	if err != nil {
		return 0, fmt.Errorf("--concurrency %q is neither a number nor \"max\"", flag)
	}
	if n < 1 {
		return 0, fmt.Errorf("--concurrency must be at least 1, got %d", n)
	}
	return n, nil
}

// componentPlan is one selected component with everything resolved that has to
// be known before anything starts.
//
// The stack is loaded here rather than inside the run because a stack is the
// only thing that knows which host ports its component publishes, and the
// scheduler needs every component's before it can decide which of them may run
// together. Loading up front also moves compose's own validation — a
// compose.up naming a service the file does not declare, a wait: healthy with
// no healthcheck — ahead of the first container, where it costs nothing.
type componentPlan struct {
	c     component.Component
	log   *componentLog
	stack *compose.Stack // nil when the component declares no services
	ports []int
	// row is already final when the component cannot be run at all, and the
	// scheduler is never given it.
	row   ui.Row
	ready bool
}

// componentLogs is the teststages.Logs this command runs with: each component
// writes to its own componentLog under root, mirrored to stderr when stream is
// set.
//
// closeAll closes them in the reverse of the order they were opened. Closing a
// streamed log flushes the unterminated line its mirror still holds, so that
// order is the order those last lines reach the terminal.
func componentLogs(root, kind string, stream bool) (testrun.Opener, func()) {
	var opened []*componentLog
	open := func(c component.Component, width int, runs bool) testrun.Output {
		log := logFor(root, kind, stream, c, width, runs)
		opened = append(opened, log)
		return logOutput(log)
	}
	closeAll := func() {
		for _, log := range slices.Backward(opened) {
			log.Close()
		}
	}
	return open, closeAll
}

// itemFor is testrun.ItemFor.
func itemFor(p componentPlan) scheduler.Item { return testrun.ItemFor(runPlan(p)) }

// planComponents is testrun.PlanComponents, with each component's output
// opened as this command's componentLog.
func planComponents(ctx context.Context, root string, selected []component.Component, kind string, stream bool) []componentPlan {
	plans, logs := openPlans(ctx, root, selected, kind, stream)
	out := make([]componentPlan, len(plans))
	for i, p := range plans {
		out[i] = componentPlan{c: p.C, log: logs[p.C.Name], stack: p.Stack, ports: p.Ports, row: p.Row, ready: p.Ready}
	}
	return out
}

// openPlans is testrun.PlanComponents with an opener that gives each component
// a componentLog, and the logs it opened, by component name — the one key a
// declaration guarantees unique.
func openPlans(ctx context.Context, root string, selected []component.Component, kind string, stream bool) ([]testrun.Plan, map[string]*componentLog) {
	logs := make(map[string]*componentLog, len(selected))
	plans := testrun.PlanComponents(ctx, root, selected, kind, func(c component.Component, width int, runs bool) testrun.Output {
		log := logFor(root, kind, stream, c, width, runs)
		logs[c.Name] = log
		return logOutput(log)
	})
	return plans, logs
}

// logFor is the componentLog one component writes to under kind.
//
// A component declaring no suite gets a log that writes nowhere: an empty file
// under its name would read as the output of a run that never happened.
func logFor(root, kind string, stream bool, c component.Component, width int, runs bool) *componentLog {
	if !runs {
		return &componentLog{out: io.Discard, name: c.Name, width: width}
	}
	return openLog(root, c.Name, kind+".log", stream, width)
}

// runPlan is p as the engine plans one.
func runPlan(p componentPlan) testrun.Plan {
	return testrun.Plan{C: p.c, Out: logOutput(p.log), Stack: p.stack, Ports: p.ports, Row: p.row, Ready: p.ready}
}

// logOutput is where log's writes go and the path a report names for them. A
// nil log has neither.
func logOutput(log *componentLog) testrun.Output {
	if log == nil {
		return testrun.Output{}
	}
	return testrun.Output{W: log.out, Rel: log.Rel}
}

// scheduleRow is testrun.ScheduleRow.
func scheduleRow(ctx context.Context, outcome scheduler.Outcome, components, limit int) ui.Row {
	return testrun.ScheduleRow(ctx, outcome, components, limit)
}

// clearReport is testrun.ClearReport.
func clearReport(dir, report string) error { return testrun.ClearReport(dir, report) }

// failure is testrun.Failure, naming log's path.
func failure(label string, log *componentLog, what, value, output string) ui.Row {
	return testrun.Failure(label, logOutput(log).Rel, what, value, output)
}

// componentLog is where one component's commands write, and where the report
// points a reader when something failed.
//
// Everything is captured, always — including under --json, where the terminal
// carries a document and nothing else, so the log is the only place the output
// exists at all. Streaming as well is the exception rather than the rule, for
// the reason RunOutput exists: a passing suite's output is thousands of lines
// nobody reads, and in a CI log it buries the one component that failed.
type componentLog struct {
	// Path is the file, absolute, so a reader can open it from anywhere and a
	// CI step can collect it by a path it knows.
	Path string
	// Rel is Path relative to the scan root, which is what a report shows: an
	// absolute path from someone else's runner is noise in a PR comment.
	Rel string

	file   *os.File
	out    io.Writer
	name   string
	width  int
	mirror *prefixWriter
}

// openLog creates the component's log, named for the command writing it: one
// component runs under `lydite test` and under `lydite mutation` alike, and a
// single file would have whichever ran second overwrite the other's output.
//
// A log that cannot be created is not a reason to skip the component, so it
// degrades to capture-only: the tail under a failing row still names the
// cause, which is most of what the file is for.
func openLog(root, name, file string, stream bool, width int) *componentLog {
	l := &componentLog{out: io.Discard, name: name, width: width}
	dir := filepath.Join(root, runner.ReportDir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", name, err)
		return l.streamed(stream)
	}
	ignoreReports(filepath.Join(root, runner.ReportDir))
	path := filepath.Join(dir, file)
	f, err := os.Create(path) // #nosec G304 -- the path is lydite's own report directory under the scan root, built from a validated component name
	if err != nil {
		fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", name, err)
		return l.streamed(stream)
	}
	l.file, l.out = f, f
	l.Path = path
	if abs, err := filepath.Abs(path); err == nil {
		l.Path = abs
	}
	l.Rel = path
	if rel, err := filepath.Rel(root, path); err == nil {
		l.Rel = rel
	}
	return l.streamed(stream)
}

// streamed mirrors the log to stderr when asked. Stderr and never stdout,
// because stdout carries the report and, under --json, a document that a
// suite's output would make unparseable.
//
// The mirror is prefixed with the component's name and the file underneath is
// not. Components run concurrently, so an unlabelled line on the terminal
// belongs to nobody in particular; the log is already the per-component view,
// and prefixing it would make it a lossy copy of what the suite printed rather
// than the thing a CI job uploads and a report links.
func (l *componentLog) streamed(stream bool) *componentLog {
	if !stream {
		return l
	}
	l.mirror = &prefixWriter{prefix: fmt.Sprintf("%-*s | ", l.width, l.name)}
	if l.file == nil {
		l.out = l.mirror
	} else {
		l.out = io.MultiWriter(l.mirror, l.file)
	}
	return l
}

// stderrMu serialises whole lines onto stderr.
//
// Without it two components writing at once interleave inside a line, and a
// prefix naming the component that wrote half of it is worse than no prefix at
// all.
var stderrMu sync.Mutex

// partialLineDelay is how long an unterminated line waits before it is shown
// anyway.
//
// Buffering to a newline is what makes a prefix meaningful, but a suite that
// prints `running 412 tests...` and then hangs has written no newline — and
// watching a hang is the one thing --stream exists for, so holding that line
// until the process is killed withholds exactly the output somebody turned the
// flag on to see. Long enough that ordinary output is not split mid-line,
// short enough that a person watching a stalled run does not conclude nothing
// was printed.
const partialLineDelay = 500 * time.Millisecond

// prefixWriter labels each complete line with its component and writes it to
// stderr under stderrMu.
//
// It buffers until a newline because a writer is handed whatever chunk the
// child happened to flush, which is not a line: prefixing each chunk would
// scatter the label through the middle of the output.
type prefixWriter struct {
	prefix string
	// onEmit replaces the write to stderr. Only a test sets it: capturing the
	// process's own stderr is not something two tests can do at once.
	onEmit func(line []byte)
	// delay overrides partialLineDelay. Only a test sets it: an assertion
	// about whole lines must not depend on the machine having got round to
	// the next write inside the production deadline.
	delay time.Duration

	mu  sync.Mutex
	buf []byte
	// gen invalidates a deadline that no longer belongs to what is pending.
	// Timer.Stop reports false once the callback has fired, and that callback
	// may be blocked on mu inside this very Write — so it cannot be cancelled,
	// only ignored when it finally runs. Acting on it would flush a line that
	// began after it was scheduled and drop the live timer's handle.
	//
	// The interleaving this guards cannot be forced from a test without a
	// synchronisation hook in this type, so no test covers it; the deadline's
	// ordinary behaviour is covered by TestTheDeadlineFollowsThePendingLine.
	gen   uint64
	timer *time.Timer
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	emitted := false
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
		emitted = true
	}
	// A deadline armed for a line that has since been completed does not
	// belong to what is pending now: it would fire early and split the new
	// line, which is the thing buffering exists to prevent. The deadline
	// belongs to the line that is waiting, so it restarts whenever a
	// different one starts waiting.
	if emitted {
		w.disarm()
	}
	w.arm()
	return len(p), nil
}

// arm schedules the leftover of a line to be shown if nothing completes it.
// Called with w.mu held.
//
// The deadline runs from when the pending line began, not from the last write.
// Restarting it on every write would measure silence instead, and a runner
// redrawing a progress display with a carriage return and no newline writes
// every few tens of milliseconds for minutes — so the deadline would never
// expire, nothing would reach the terminal, and the buffer would hold every
// byte of it.
func (w *prefixWriter) arm() {
	if len(w.buf) == 0 {
		w.disarm()
		return
	}
	if w.timer != nil {
		return
	}
	gen := w.gen
	w.timer = time.AfterFunc(w.partialLineDelay(), func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		// A deadline from before the last disarm has already fired and is
		// only now getting the lock. Acting on it would flush a line that
		// started after it was scheduled — the mid-line split this all
		// exists to prevent — and would drop the live timer's handle.
		if gen != w.gen {
			return
		}
		w.timer = nil
		w.flush()
	})
}

// disarm invalidates the pending deadline. Called with w.mu held.
func (w *prefixWriter) disarm() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.gen++
}

// partialLineDelay is how long this writer waits; a test sets its own so the
// assertion does not depend on how fast the machine ran the loop.
func (w *prefixWriter) partialLineDelay() time.Duration {
	if w.delay > 0 {
		return w.delay
	}
	return partialLineDelay
}

// Flush writes a trailing line that never ended in a newline, which is what a
// suite killed part-way through printing leaves behind — and is the line most
// worth seeing.
func (w *prefixWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.disarm()
	w.flush()
}

// flush is called with w.mu held.
func (w *prefixWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

func (w *prefixWriter) emit(line []byte) {
	if w.onEmit != nil {
		w.onEmit(line)
		return
	}
	stderrMu.Lock()
	defer stderrMu.Unlock()
	fmt.Fprintf(os.Stderr, "%s%s\n", w.prefix, line)
}

func (l *componentLog) Close() {
	if l.mirror != nil {
		l.mirror.Flush()
	}
	if l.file != nil {
		_ = l.file.Close()
	}
}

// tailLines is testrun.TailLines.
const tailLines = testrun.TailLines

// tail is testrun.Tail.
func tail(output string) []string { return testrun.Tail(output) }

// startServices is testrun.StartServices.
func startServices(ctx context.Context, p componentPlan, label string) (func(), ui.Row, bool) {
	return testrun.StartServices(ctx, runPlan(p), label)
}

// runCommands is testrun.RunCommands, writing to log.
func runCommands(ctx context.Context, dir, label string, c component.Component, tc *toolchain.Env, kind string, cmds []string, log *componentLog) (ui.Row, bool) {
	return testrun.RunCommands(ctx, dir, label, c, tc, kind, cmds, logOutput(log))
}

// prepare is testrun.Prepare, writing to log.
func prepare(ctx context.Context, inv runner.Invocation, dir, root, label string, c component.Component, cfg config.Config, tc *toolchain.Env, log *componentLog) (ui.Row, bool) {
	return testrun.Prepare(ctx, inv, dir, root, label, c, cfg, tc, logOutput(log))
}

// invocation is testrun.Invocation.
func invocation(c component.Component, variant runner.Variant) (runner.Invocation, error) {
	return testrun.Invocation(c, variant)
}

// flakyLabel is testrun.FlakyLabel.
func flakyLabel(name string) string { return testrun.FlakyLabel(name) }

// childEnv is testrun.ChildEnv.
func childEnv(tc *toolchain.Env, c component.Component, inv runner.Invocation) []string {
	return testrun.ChildEnv(tc, c, inv)
}

// splitPath is testrun.SplitPath.
func splitPath(declared []string) (dirs, vars []string) { return testrun.SplitPath(declared) }

// env is testrun.Env.
func env(c component.Component) []string { return testrun.Env(c) }

// componentUnits is testrun.ComponentUnits.
func componentUnits(components []component.Component) []toolchain.Unit {
	return testrun.ComponentUnits(components)
}

// orphanRow is testrun.OrphanRow, warning on stderr.
func orphanRow(ctx context.Context, dir string, file component.File) ui.Row {
	return testrun.OrphanRow(ctx, dir, file, os.Stderr)
}

// renderReport writes the run's document, renders the terminal or JSON
// report, and returns the verdict as an exit code.
func renderReport(cmd *cobra.Command, rep *ui.Report, root string, asJSON, noColor bool) error {
	saveDocument(root, rep)
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}

// affectedFrom is testrun.AffectedFrom.
func affectedFrom(ctx context.Context, dir string, file component.File, base string) (affected.Result, error) {
	return testrun.AffectedFrom(ctx, dir, file, base)
}

// selectRow is testrun.SelectRow.
func selectRow(res affected.Result, declared int) ui.Row { return testrun.SelectRow(res, declared) }

// intersect is testrun.Intersect.
func intersect(cs []component.Component, own []component.Component) []component.Component {
	return testrun.Intersect(cs, own)
}
