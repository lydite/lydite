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
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
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
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			file, err := component.Load(dir)
			if err != nil {
				return err
			}
			// Before selection and before anything runs, because the gate
			// asks whether the declaration is complete and that question
			// does not depend on which components this invocation chose —
			// or on there being any. A repository that declares none is
			// exactly the one whose every source file is orphaned, and a
			// gate it never saw would be the failure it exists to catch.
			rep.Add(orphanRow(ctx, dir, file))

			// Before selection: a watch pattern that fires for nothing is a
			// component that stops running when its input changes, and the
			// question is about the declaration rather than about what this
			// invocation chose.
			rep.Add(watchRow(ctx, dir, file))

			// The responsibility set: what this run reports on, and the
			// whole of it. `--component` says what this job is responsible
			// for and `--affected` says which of those need running, so the
			// two compose rather than competing — a shard runs
			// `--affected --component <slice>` and reports one row per
			// component in the slice, whether or not selection ran it.
			//
			// A run reports nothing at all about a component outside this
			// set. Under a matrix that is what keeps every declared component
			// appearing exactly once across the shards, which is the property
			// `lydite test merge` decides completeness from.
			own, err := file.Select(components)
			if err != nil {
				return err
			}
			// Narrowed by --component, which is what makes a repository-wide
			// figure unanswerable here: coverage(repo) and patch(repo) sum
			// every component, and a shard holding two of four would publish
			// its own two under a label about the repository. `lydite test
			// merge` emits them once, from every shard's measurements.
			narrowed := len(components) > 0
			selected := own
			// Before any suite runs. `lydite test` is the command that invokes
			// `go test`, `cargo llvm-cov` and `npx vitest`, so it is the one
			// that has to make their toolchains present — a runner with no
			// Node answers `npx: not found` rather than provisioning one, and
			// a runner whose ambient Go is fine still needs GOTOOLCHAIN
			// pinned, which is the whole reason internal/toolchain touches Go
			// at all.
			//
			// It is resolved here rather than inherited from a `lydite scan`
			// earlier in the same job: the result is a value handed to each
			// component's own commands, not a change to this process.
			envs, err := ensureToolchains(ctx, cmd, dir, cfg, componentUnits(own))
			if err != nil {
				return err
			}
			// Empty unless selection ran: with no skipped components the
			// declaration order of the selected set is already the order of
			// the whole set.
			var skipped map[string]ui.Row
			var ordered []component.Component
			// Nothing to select from is not the same as a change that
			// selected nothing, and selection must not be asked which it is.
			// Running it would render a select row claiming the diff was
			// empty beside a row saying no components are declared — two rows
			// contradicting each other — and would pay a git fetch to do it,
			// which on a shallow or fork checkout turns a report that renders
			// into a hard error before any row is written.
			if onlyAffected && len(file.Components) > 0 {
				var res affected.Result
				res, err = selectAffected(ctx, dir, file, baseBranch)
				if err != nil {
					return err
				}
				// Selection is computed over the whole declaration and
				// intersected with this run's responsibility set afterwards,
				// so the `select` row says the same thing in every shard.
				// Computing it over the slice would give each shard its own
				// counts and reasons, and the fold has no way to tell that
				// from shards that saw different trees.
				selected = intersect(res.Selected, own)
				rep.Add(selectRow(res, len(file.Components)))
				// The same label shape a suite row takes, so every component
				// produces exactly one test(<name>) row whether it ran or
				// not. A bare name would collide with a gate row for a
				// component called "watch", "select", "orphans" or
				// "schedule" — nothing forbids those — and a consumer keying
				// rows by label silently loses the gate.
				//
				// They are handed to runComponents rather than added here so
				// the rows interleave into declaration order. Added here they
				// would all precede the suites, and a declaration of a, b, c
				// with only b affected would document b last.
				//
				// Scoped to the responsibility set, so `test(a): not
				// affected` is emitted by the one shard that owns a.
				//
				// A component declaring no suite takes the row its declaration
				// gives it whether or not selection reached it, so the row
				// reads the same here as in a fold, where no shard ran it.
				skipped = make(map[string]ui.Row, len(res.Skipped))
				for _, c := range intersect(res.Skipped, own) {
					if declaresNoSuite(c) {
						skipped[c.Name] = noSuiteTestRow("test", c.Name)
						continue
					}
					skipped[c.Name] = ui.Row{Status: ui.StatusUnmeasured, Label: testLabel(c.Name), Value: "not affected"}
				}
				ordered = own
			}
			// Resolved once for the run rather than once per component: the
			// merge-base and the paths the change touched are facts about the
			// change, and asking git for them per component pays the same walk
			// again for every one of them.
			gate := testrun.NewFlakyGate(ctx, dir, baseBranch, gateFlaky)
			cov := coverageOptions{
				Instrument:  !noCoverage,
				Gate:        gateCoverage,
				BaseBranch:  baseBranch,
				Concurrency: limit,
				Selected:    onlyAffected,
				Narrowed:    narrowed,
			}
			if len(selected) == 0 {
				// Nothing ran, so nothing interleaves: the skipped rows are
				// the whole set, still in declaration order.
				for _, c := range own {
					if r, ok := skipped[c.Name]; ok {
						rep.Add(r)
					}
				}
				// A row per component whether or not anything ran, for the
				// reason the coverage rows below take one: a component whose
				// suite never ran examined none of its new tests, and a
				// section that disappears reads as a concern that passed.
				addFlakyRows(rep, gate, own)
				// The gate still reports, because a caller that asked for it
				// has to be able to tell this from a run where the flag was
				// dropped. On the default branch this is also the run that
				// records: a tree matching its base selects nothing and is
				// still the tree whose coverage the next change gates
				// against.
				if len(file.Components) > 0 {
					addTestCoverageRows(ctx, cmd, rep, dir, file, own, nil, cfg, cov)
				}
				// Only when the declaration is genuinely empty. A run that
				// selected nothing has components declared and has already
				// said so through its own select row, and repeating the
				// sentence here would have the report contradict itself
				// about the one thing it exists to state.
				if len(file.Components) == 0 {
					// Through the report rather than around it: --json
					// promises stdout carries a document and nothing else,
					// and a bare sentence printed here is unparseable output.
					rep.Add(ui.Row{
						Status: ui.StatusUnmeasured,
						Label:  "test",
						Value:  "no components declared in " + component.FileName,
					})
				}
				return renderReport(cmd, rep, dir, asJSON, noColor)
			}

			ms := runComponentsGated(ctx, dir, selected, ordered, skipped, cfg, envs, limit, stream, cov.Instrument, rep, gate)
			addFlakyRows(rep, gate, own)
			addTestCoverageRows(ctx, cmd, rep, dir, file, own, ms, cfg, cov)
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

// withSuites is the components that declare a suite, in the order given.
func withSuites(cs []component.Component) []component.Component {
	out := make([]component.Component, 0, len(cs))
	for _, c := range cs {
		if !declaresNoSuite(c) {
			out = append(out, c)
		}
	}
	return out
}

// addTestCoverageRows is addCoverageRows over the components that declare a
// suite, followed by the declaration's rows for each that declares none.
//
// A component declaring no suite is kept out of the measured set rather than
// handed to it as unmeasurable: a measurement with no language renders its
// complexity row as a raw command's, naming a command this component does not
// declare. It contributes to no composed figure either way — nothing could
// ever measure it, so it sits on neither side of any comparison.
//
// Its rows follow only a run that instruments, for the reason a suite's do: a
// run under --no-coverage emits no coverage row for any component.
func addTestCoverageRows(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir string, file component.File, own []component.Component, ms []measurement, cfg config.Config, cov coverageOptions) {
	if suites := withSuites(own); len(suites) > 0 {
		addCoverageRows(ctx, cmd, rep, dir, file, suites, ms, cfg, cov)
	}
	if !cov.Instrument {
		return
	}
	for _, c := range own {
		if declaresNoSuite(c) {
			noSuiteCoverageRows(rep, c.Name, cfg.Coverage.Floor)
		}
	}
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

// runComponents plans every selected component, runs them under the scheduler
// and adds their rows in declaration order. It is runComponentsGated with no
// flaky gate.
func runComponents(ctx context.Context, root string, selected, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, limit int, stream, instrument bool, rep *ui.Report) []measurement {
	return runComponentsGated(ctx, root, selected, ordered, skipped, cfg, envs, limit, stream, instrument, rep, nil)
}

// runComponentsGated plans every selected component through this command's
// logs, runs them with testrun.RunComponents, and adds the rows it returns to
// rep in the order it returns them. Every log is closed once the run is over.
func runComponentsGated(ctx context.Context, root string, selected, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, limit int, stream, instrument bool, rep *ui.Report, gate *testrun.FlakyGate) []measurement {
	plans, logs := openPlans(ctx, root, selected, "test", stream)
	for _, p := range plans {
		defer logs[p.C.Name].Close()
	}
	rows, measured := testrun.RunComponents(ctx, root, plans, ordered, skipped, cfg, envs, limit, instrument, gate)
	for _, row := range rows {
		rep.Add(row)
	}
	return measured
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
//
// A component declaring no suite gets a log that writes nowhere: an empty file
// under its name would read as the output of a run that never happened.
func openPlans(ctx context.Context, root string, selected []component.Component, kind string, stream bool) ([]testrun.Plan, map[string]*componentLog) {
	logs := make(map[string]*componentLog, len(selected))
	plans := testrun.PlanComponents(ctx, root, selected, kind, func(c component.Component, width int, runs bool) testrun.Output {
		log := &componentLog{out: io.Discard, name: c.Name, width: width}
		if runs {
			log = openLog(root, c.Name, kind+".log", stream, width)
		}
		logs[c.Name] = log
		return logOutput(log)
	})
	return plans, logs
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

// addFlakyRows adds the flaky gate's rows for own to rep, in the order the gate
// returns them, and every finding its disagreements made.
func addFlakyRows(rep *ui.Report, gate *testrun.FlakyGate, own []component.Component) {
	rows, found := gate.Report(own)
	for _, row := range rows {
		rep.Add(row)
	}
	rep.AddFindings(found...)
}

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

// selectAffected narrows the run to the components the change against the
// merge-base could have broken.
//
// An unresolvable merge-base is an error rather than a fallback, in either
// direction. Falling back to nothing is the failure selection exists to avoid;
// falling back to everything is safe but makes the optimisation stop happening
// with no symptom other than a slow job, which is how a shallow checkout goes
// unnoticed for months. `lydite scan --diff-base auto` refuses for the same
// reason, and a shallow checkout is a fixable misconfiguration.
func selectAffected(ctx context.Context, dir string, file component.File, baseBranch string) (affected.Result, error) {
	base, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
	if err != nil {
		// The base branch is resolved inside, so an undiscoverable one
		// already arrives here as an error naming --base-branch. What is
		// left to say is the other cause, which produces the same
		// merge-base failure from a branch that resolved perfectly.
		return affected.Result{}, fmt.Errorf("--affected needs the merge-base with the base branch, and it could not be resolved: %w"+
			"\n       a shallow checkout is the usual cause — fetch with depth 0", err)
	}
	return affectedFrom(ctx, dir, file, base)
}

// affectedFrom is testrun.AffectedFrom.
func affectedFrom(ctx context.Context, dir string, file component.File, base string) (affected.Result, error) {
	return testrun.AffectedFrom(ctx, dir, file, base)
}

// selectRow is testrun.SelectRow.
func selectRow(res affected.Result, declared int) ui.Row { return testrun.SelectRow(res, declared) }

// watchRow gates every declared watch pattern against the tree.
//
// A pattern covering no file is a component that will not run when its input
// changes — silently, permanently, and green every time. It fails where an
// unused exclude only warns, because an exclude covering nothing leaves the
// orphan gate stricter than declared while this leaves a suite unrun.
//
// Outside a git repository it reports unmeasured and passes, the same shape
// orphanRow takes: a gate that could not run must be visibly distinct from one
// that passed, and turning `lydite test` in an exported tarball into a hard
// failure would be the gate firing on ordinary work.
func watchRow(ctx context.Context, dir string, file component.File) ui.Row {
	const label = "watch"
	declared := 0
	for _, c := range file.Components {
		declared += len(c.Watch)
	}
	if declared == 0 {
		return ui.Row{Status: ui.StatusPass, Label: label, Value: "none declared"}
	}
	files, err := gitdiff.Tracked(ctx, dir)
	if errors.Is(err, gitdiff.ErrNoRepository) {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no git repository"}
	}
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not checked", Detail: []string{err.Error()}}
	}
	// A scan root git lists nothing for — one that is itself ignored, a
	// vendored checkout, a --dir pointed at build output — sits inside a work
	// tree and exits zero. Every pattern would read as covering no file, and
	// the gate would fail a declaration that is correct while claiming the
	// author had written a typo. The same case orphanRow reports through
	// ErrNoFiles.
	if len(files) == 0 {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no files found"}
	}
	unmatched := affected.UnmatchedWatch(file, files)
	if len(unmatched) == 0 {
		return ui.Row{Status: ui.StatusPass, Label: label, Value: fmt.Sprintf("%d pattern(s) match", declared)}
	}
	detail := make([]string, 0, len(unmatched)+1)
	for _, u := range unmatched {
		detail = append(detail, fmt.Sprintf("%s: %q covers no file", u.Component, u.Pattern))
	}
	// The rule rather than a guess at what was meant, the same stance the
	// unused-exclude warning takes: a pattern whose file was deleted has no
	// better spelling at all.
	detail = append(detail, "patterns are anchored, so a subtree is spelled \"dir/**\"; correct or remove each pattern in "+component.FileName)
	return ui.Row{
		Status: ui.StatusFail,
		Label:  label,
		Value:  fmt.Sprintf("%d of %d pattern(s) cover no file", len(unmatched), declared),
		Detail: detail,
	}
}

// intersect is testrun.Intersect.
func intersect(cs []component.Component, own []component.Component) []component.Component {
	return testrun.Intersect(cs, own)
}
