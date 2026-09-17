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
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/flaky"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
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
				skipped = make(map[string]ui.Row, len(res.Skipped))
				for _, c := range intersect(res.Skipped, own) {
					skipped[c.Name] = ui.Row{Status: ui.StatusUnmeasured, Label: testLabel(c.Name), Value: "not affected"}
				}
				ordered = own
			}
			// Resolved once for the run rather than once per component: the
			// merge-base and the paths the change touched are facts about the
			// change, and asking git for them per component pays the same walk
			// again for every one of them.
			gate := newFlakyGate(ctx, dir, baseBranch, gateFlaky)
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
				gate.report(rep, own)
				// The gate still reports, because a caller that asked for it
				// has to be able to tell this from a run where the flag was
				// dropped. On the default branch this is also the run that
				// records: a tree matching its base selects nothing and is
				// still the tree whose coverage the next change gates
				// against.
				if len(file.Components) > 0 {
					addCoverageRows(ctx, cmd, rep, dir, file, own, nil, cfg, cov)
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
			gate.report(rep, own)
			addCoverageRows(ctx, cmd, rep, dir, file, own, ms, cfg, cov)
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

// testLabel is how every row about one component's suite is named.
//
// Labelled rather than bare, so every component produces exactly one
// test(<name>) row whether it ran or not, and so a component called `watch`,
// `select`, `orphans` or `schedule` cannot take a gate row's label. Nothing
// forbids those names, and a consumer keying rows by label would silently lose
// the gate.
func testLabel(name string) string { return "test(" + name + ")" }

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
// and adds their rows in declaration order.
//
// Declaration order and never completion order: a reader diffing two runs
// depends on it, and ordering by whichever finished first would put this run's
// timing into the document, so two runs of the same declaration would produce
// different reports.
//
// An interrupted run fails through the `schedule` row rather than through a
// returned error. The unstarted rows are `unmeasured`, which does not vote, so
// a run cut short by a CI job timeout would otherwise carry no failing row —
// and `--json` would publish `"verdict": "pass"` for a run that tested half the
// repository. Anything automated reads that document and never the terminal, so
// a truncation visible only in the process exit code is a truncation the PR
// comment renders green. `ui.Report.ExitCode` stays the single place the
// mapping lives.
func runComponents(ctx context.Context, root string, selected, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, limit int, stream, instrument bool, rep *ui.Report) []measurement {
	return runComponentsGated(ctx, root, selected, ordered, skipped, cfg, envs, limit, stream, instrument, rep, nil)
}

// runComponentsGated is that run with the flaky gate a caller asked for, which
// each component's own run carries out before its services are torn down. A
// nil gate is a run nobody asked one of.
func runComponentsGated(ctx context.Context, root string, selected, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, limit int, stream, instrument bool, rep *ui.Report, gate *flakyGate) []measurement {
	plans := planComponents(ctx, root, selected, "test", stream)
	for _, p := range plans {
		defer p.log.Close()
	}

	rows := make([]ui.Row, len(plans))
	measured := make([]measurement, len(plans))
	for i, p := range plans {
		measured[i] = unmeasuredComponent(p.c, "the component did not run")
	}
	var items []scheduler.Item
	var index []int
	for i, p := range plans {
		if !p.ready {
			rows[i] = p.row
			continue
		}
		// Pre-filled, so a component the run never reached reports that it
		// did not run rather than being dropped. A truncated run that simply
		// omitted rows would read as a complete run over fewer components,
		// and a check that could not run must never read as one that did.
		rows[i] = ui.Row{
			Status: ui.StatusUnmeasured,
			Label:  testLabel(p.c.Name),
			Value:  "not run",
			Detail: []string{"the run ended before this component started"},
		}
		items = append(items, itemFor(p))
		index = append(index, i)
	}

	outcome := scheduler.Run(ctx, items, limit, func(ctx context.Context, k int) {
		i := index[k]
		rows[i], measured[i] = runComponent(ctx, root, plans[i], cfg, envs.For(plans[i].c.Name), instrument, gate)
	})

	// A cancelled run kills every suite it had started, so each one exits
	// non-zero and would otherwise be reported as a test failure — four
	// components' worth of red rows attributing a CI job timeout to the
	// repository's tests, in the document the PR comment reads. Under
	// cancellation lydite cannot tell a suite that failed from one that was
	// killed, and saying so is the only honest answer. A component that had
	// already passed keeps its result, because that one is not in doubt.
	if ctx.Err() != nil {
		// Only over what the scheduler dispatched. A row built during
		// planning — a compose file that would not load, a compose.up naming
		// a service the file does not declare — was final before the run
		// began, and is the one actionable error such a run produced;
		// rewriting it would discard the answer in favour of a sentence about
		// an interrupt that had nothing to do with it.
		for _, i := range index {
			r := rows[i]
			// Only a failing row is in doubt. A component that had already
			// passed produced a real result, and one that never started
			// already says so — rewriting that would tell a reader a
			// component did not finish something it never began.
			if r.Status != ui.StatusFail {
				continue
			}
			rows[i] = ui.Row{
				Status: ui.StatusUnmeasured,
				Label:  r.Label,
				Value:  "not completed",
				Detail: []string{"the run was interrupted before this component finished"},
				Log:    r.Log,
			}
			measured[i] = unmeasuredComponent(plans[i].c, "the run was interrupted before this component finished")
		}
	}

	// Only a component that passed contributes a measurement. A report
	// written by a suite that failed, was killed, or never started describes
	// an unfinished run, and recording it would put a number in the baseline
	// that nothing can be compared against honestly. Enforced here, over the
	// final rows, so no path out of runComponent can forget it.
	for i := range plans {
		if rows[i].Status != ui.StatusPass {
			// The row's own words, so the coverage row and the suite row give
			// a reader the same account of one event rather than two.
			replaced := unmeasuredComponent(plans[i].c, rows[i].Label+" did not pass: "+rows[i].Value)
			// The test counts survive, and only they. The rule above is about
			// a MEASUREMENT of the tree: a coverage report from a suite that
			// stopped early describes an unfinished run, so nothing may be
			// gated against it. A JUnit report is not a measurement of the
			// tree at all — it says how many tests ran and how many went red,
			// which is exactly what happened, and is the data point a quality
			// history most wants from a commit whose build broke.
			replaced.Tests, replaced.TestsWhy = measured[i].Tests, measured[i].TestsWhy
			measured[i] = replaced
		}
	}

	rep.Add(scheduleRow(ctx, outcome, len(plans), limit))
	if len(skipped) == 0 {
		for _, r := range rows {
			rep.Add(r)
		}
		return measured
	}
	// Interleaved by the declaration, so a component selection skipped sits
	// where its author wrote it rather than ahead of every component that
	// ran. Two runs over one declaration already produce the same document;
	// this is what makes that document read in the order the file does.
	byName := make(map[string]ui.Row, len(rows))
	for _, r := range rows {
		byName[r.Label] = r
	}
	for _, c := range ordered {
		if r, ok := skipped[c.Name]; ok {
			rep.Add(r)
			continue
		}
		if r, ok := byName[testLabel(c.Name)]; ok {
			rep.Add(r)
		}
	}
	return measured
}

// itemFor is what the scheduler locks on: the component's root, cleaned so
// `./web`, `web/` and `web` are one directory, and the host ports its services
// publish.
//
// One construction, because a second one that agreed today would come apart
// the day the normalisation changed — and the test built on the copy would
// keep passing.
func itemFor(p componentPlan) scheduler.Item {
	return scheduler.Item{Name: p.c.Name, Dir: path.Clean(p.c.Dir), Ports: p.ports}
}

// planComponents opens each component's log and loads the stack of any that
// declares services.
//
// The container runtime is probed at most once for the whole run, and only
// when something actually declares services: a component that declares none
// needs no runtime, so a repository without services still runs on a machine
// with no container engine at all.
func planComponents(ctx context.Context, root string, selected []component.Component, kind string, stream bool) []componentPlan {
	width := 0
	for _, c := range selected {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}

	// Probed at most once for the whole run, and only if a declaration gets
	// far enough to need it: compose.LoadWith consults this after validating,
	// so a compose.up naming a service the file does not declare is reported
	// as that rather than as the machine having no container engine.
	var (
		rt       compose.Runtime
		probeErr error
		probed   bool
	)
	runtime := func() (compose.Runtime, error) {
		if !probed {
			probed = true
			rt, probeErr = compose.Probe(ctx)
			if probeErr == nil {
				fmt.Fprintf(os.Stderr, "lydite: services via %s\n", rt)
			}
		}
		return rt, probeErr
	}

	plans := make([]componentPlan, len(selected))
	for i, c := range selected {
		p := componentPlan{c: c, log: openLog(root, c.Name, kind+".log", stream, width), ready: true}
		if !c.Compose.Declared() {
			plans[i] = p
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(c.Dir))
		stack, err := compose.LoadWith(runtime, dir, c, p.log.out)
		if err != nil {
			// A probe runs candidate `docker compose version` invocations
			// under the run's context, so an interrupt kills every one of
			// them and looks exactly like a machine with no container
			// runtime. Reporting that would be a confident wrong answer
			// about the machine, and would report an interrupted run as a
			// component that failed — the plan stays runnable instead, and
			// the scheduler starts nothing on a cancelled context, so it
			// lands on the `not run` row where it belongs.
			p.ready = false
			if ctx.Err() != nil {
				// Not the compose error: a probe runs candidate `docker
				// compose version` invocations under the run's context, so an
				// interrupt kills every one of them and looks exactly like a
				// machine with no container runtime. Reporting that would be
				// a confident wrong answer about the machine.
				//
				// Marked unrunnable rather than left runnable-with-no-stack,
				// which is byte-for-byte the state of a component that
				// declares no services at all — safe only for as long as the
				// scheduler refuses to admit anything on a cancelled context,
				// and a suite started without the database it declared is the
				// one outcome worse than not running it.
				p.row = ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  kind + "(" + c.Name + ")",
					Value:  "not run",
					Detail: []string{"the run ended before this component started"},
				}
			} else {
				p.row = failure(kind+"("+c.Name+")", p.log, err.Error(), "services not started", "")
			}
			plans[i] = p
			continue
		}
		p.stack, p.ports = stack, stack.HostPorts()
		plans[i] = p
	}
	return plans
}

// scheduleRow says what the scheduler actually did.
//
// The concurrency reached is observed rather than asserted, and it is the
// number that separates a scheduler that ran from one that only claims to:
// every port-lock assertion is satisfied by a run that never had two
// components going at once, because the lock is never taken. The conflicting
// pairs are named beside it, so a reader — and the proving ground's assertion
// — can see the constraint was reached rather than merely declared.
//
// A run that did not start every component it was given fails here, and this
// is the only row that can carry that. The components that never ran are
// `unmeasured`, which does not vote, and the ones that did run passed — so
// without a failing row a truncated run publishes a passing verdict, and the
// gate reads green having tested part of the repository.
// components is every component the run was given, including any that could
// not be planned. Both halves of the ratio come from that one set: a component
// whose stack would not load never started either, so `Started of components`
// describes a real pair of numbers, where mixing in the count that reached the
// scheduler would describe no set at all.
//
// Cancellation and not `Started < components` is what makes this fail. A run
// interrupted once everything had started has nothing left unstarted, and that
// is exactly the run whose components were all killed mid-suite — the case
// most in need of a row saying the run was cut short.
func scheduleRow(ctx context.Context, outcome scheduler.Outcome, components, limit int) ui.Row {
	row := ui.Row{Status: ui.StatusPass, Label: "schedule"}
	if ctx.Err() != nil {
		row.Status = ui.StatusFail
		row.Value = fmt.Sprintf("interrupted after %d of %d component(s)", outcome.Started, components)
	} else {
		row.Value = fmt.Sprintf("%d component(s), max %d concurrent", components, outcome.MaxConcurrent)
	}
	if pairs := scheduler.Pairs(outcome.Conflicts); pairs > 0 {
		row.Value += fmt.Sprintf(", %d pair(s) serialised", pairs)
	}
	for _, c := range outcome.Conflicts {
		row.Detail = append(row.Detail,
			fmt.Sprintf("%s and %s serialised on %s", c.A, c.B, c.On))
	}
	if limit == 1 && components > 1 {
		row.Detail = append(row.Detail, "--concurrency 1: components ran one at a time")
	}
	return row
}

// runComponent runs one component's suite and returns its row.
//
// A component whose services or setup commands lydite cannot supply fails
// rather than running: a suite executed without the database it declared
// reports failures naming the tests instead of the missing service, and a
// green run is worse still — it would mean the declaration was ignored and
// nobody was told.
func runComponent(ctx context.Context, root string, p componentPlan, cfg config.Config, tc *toolchain.Env, instrument bool, gate *flakyGate) (row ui.Row, m measurement) {
	c, log := p.c, p.log
	label := testLabel(c.Name)
	m = unmeasuredComponent(c, "the component did not run")

	variant := runner.Plain
	if instrument {
		variant = runner.Instrumented
	}
	inv, err := invocationFor(c, variant, gate)
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}, m
	}

	dir := filepath.Join(root, filepath.FromSlash(c.Dir))
	// Prepared before the suite runs, so what is measured afterwards is what
	// this run wrote.
	for _, report := range []string{inv.CoverageReport, inv.JUnitReport} {
		if report == "" {
			continue
		}
		if err := clearReport(dir, report); err != nil {
			return failure(label, log, err.Error(), "not runnable", ""), m
		}
	}
	if prepared, ok := prepare(ctx, inv, dir, label, c, cfg, tc, log); !ok {
		return prepared, m
	}

	// Deferred before anything starts, so every path out of here tears the
	// stack down — including a setup that failed halfway, which is when a
	// half-applied migration most needs undoing.
	stop, started, ok := startServices(ctx, p, label)
	if !ok {
		return started, m
	}
	defer stop()
	defer func() {
		// Teardown gets a context of its own, because the run's may already
		// be cancelled and a cancelled teardown is the leak this prevents.
		failed, ok := runCommands(context.WithoutCancel(ctx), dir, label, c, tc, "teardown", c.Teardown, log)
		// A failing teardown turns a passing component into a failing one —
		// it has left state behind that the next run will inherit — but it
		// never masks a failure that already happened, because the earlier
		// one is what the reader has to act on.
		if !ok && row.Status == ui.StatusPass {
			row = failed
		}
	}()

	if failed, ok := runCommands(ctx, dir, label, c, tc, "setup", c.Setup, log); !ok {
		return failed, m
	}

	res := executil.RunOutput(ctx, dir, childEnv(tc, c, inv), log.out, inv.Name, inv.Args...)
	// Here, and not in a later pass over the report: the stack this component
	// declared is still up, and a service-dependent test rerun after teardown
	// fails because nothing is listening — which would report the most
	// reliable test in the repository as flaky, with evidence its author
	// cannot reproduce. Whether the suite passed or failed, because it is when
	// the suite failed that a disagreement is most likely and most valuable.
	gate.run(ctx, root, dir, c, inv, tc, log)
	if !res.Ok() {
		// No measurement from a suite that failed. A report written by a run
		// that did not finish describes the tests that got as far as running,
		// and gating on it would let a broken suite record a baseline nothing
		// can be compared against honestly.
		//
		// The test counts are read anyway, and that is not the same
		// concession. They are not a baseline and gate nothing; they are what
		// the ledger records about this commit, and a commit whose suite went
		// red is the one whose counts most want recording. A failure that
		// wrote no report at all reports none, exactly as a pass would.
		failed := unmeasuredComponent(c, "the suite failed, so its coverage report describes an unfinished run")
		withTestCounts(&failed, dir, inv)
		return failure(label, log, strings.Join(append([]string{inv.Name}, inv.Args...), " ")+" in "+c.Dir, "failed", res.Output), failed
	}
	passed := measure(ctx, root, c, inv, tc, instrument)
	withTestCounts(&passed, dir, inv)
	return ui.Row{Status: ui.StatusPass, Label: label, Value: "passed", Log: log.Rel}, passed
}

// withTestCounts reads the JUnit report the invocation asked for, and says why
// there is none when there is not.
//
// A reason and never silence. A component contributing no test counts is
// indistinguishable, in a history, from one that ran no tests — and the
// commonest cause is a repository whose own runner configuration sent the
// report somewhere lydite does not look, which is a thing its author can fix
// once they are told.
func withTestCounts(m *measurement, dir string, inv runner.Invocation) {
	// Silent, and not a reason. A runner that writes no report is a property
	// of the declaration rather than something its author left undone, and a
	// line about it on every run is how a diagnostic earns a reader who skims
	// past the ones that matter.
	if inv.JUnitReport == "" {
		return
	}
	counts, err := junit.ReadFile(filepath.Join(dir, filepath.FromSlash(inv.JUnitReport)))
	if err != nil {
		m.TestsWhy = "the test report was not written: " + err.Error()
		return
	}
	if counts.Empty() {
		// Nought tests is not a suite that passed everything. It is a runner
		// that collected nothing — a filter that matched no test, a config
		// that redirected the report — and recording it as a real zero would
		// draw a line through a metric nobody measured.
		m.TestsWhy = "the test report holds no test"
		return
	}
	m.Tests = &counts
}

// clearReport makes the component's report path ready to be written to: its
// directory exists, and nothing is at the path itself.
//
// Both halves matter and neither is the other. `go test -coverprofile` fails
// outright on a path whose parent is missing, which would report an
// instrumented run as a failing suite. And a report left by an earlier run is
// what a suite that passes without writing one gets measured from — a coverage
// number describing code that is no longer there, supplied by lydite itself,
// which is the failure this gate is arranged around with lydite as its source.
//
// Every runner lydite ships happens to truncate its own report today, so this
// is a guarantee about the path rather than a fix for an observed defect in
// one of them. It is the property the measurement depends on: what is read
// back was written by the run that just finished.
func clearReport(dir, report string) error {
	// An empty report path joins to the component's directory itself, and
	// what follows would then try to remove it. Every caller is expected to
	// have refused that case already and to say something useful about it;
	// this is the floor, so a caller that forgets cannot delete a component.
	if report == "" {
		return errors.New("no coverage report path to clear")
	}
	path := filepath.Join(dir, filepath.FromSlash(report))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	ignoreReports(filepath.Join(dir, runner.ReportDir))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// failure builds a failing row: what was run, the tail of what it printed, and
// where the whole of it is.
//
// The three together are the point. The one-line invocation says what to
// re-run, the tail says why without the reader leaving the verdict, and the
// log path is what survives when the tail is not enough — and is what a CI job
// collects and a PR comment links.
func failure(label string, log *componentLog, what, value, output string) ui.Row {
	detail := []string{what}
	detail = append(detail, tail(output)...)
	if log.Rel != "" {
		detail = append(detail, "full output: "+log.Rel)
	}
	return ui.Row{Status: ui.StatusFail, Label: label, Value: value, Detail: detail, Log: log.Rel}
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

// tailLines is how much of a failing command's output goes under its row.
//
// Enough for a panic and its first frames, or a test runner's summary and the
// failure above it; small enough that a component failing does not reprint its
// whole suite. The rest is in the log, which the row names.
const tailLines = 40

// tail returns the last lines of output, for the detail under a failing row.
//
// The cause is put next to the verdict deliberately. It is already in the log
// and, when streaming, already on the terminal — but a reader looking at a red
// row should not have to scroll past another component's container lifecycle
// to find out what happened, which is exactly what a real CI log does to them.
func tail(output string) []string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil
	}
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return lines
}

// startServices brings the component's stack up and returns the teardown to
// run once the suite is done.
//
// The stack was loaded during planning, so everything that can be known
// without starting a container — that the file parses, that compose.up names
// services it declares, that a wait: healthy has a healthcheck to wait on —
// has already been decided. What is left here is the part that can only fail
// by being attempted.
//
// Failing to start is reported as the component failing rather than being
// pushed into the suite: a suite run against an absent database reports errors
// naming the tests, which is the one outcome worse than an unstarted service.
func startServices(ctx context.Context, p componentPlan, label string) (func(), ui.Row, bool) {
	if p.stack == nil {
		return func() {}, ui.Row{}, true
	}
	if err := p.stack.Up(ctx); err != nil {
		// Down anyway: up --wait leaves the containers it did start behind
		// when one of them never became healthy, and those hold the ports the
		// next component is waiting for.
		if derr := p.stack.Down(context.WithoutCancel(ctx)); derr != nil {
			fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", p.c.Name, derr)
		}
		return nil, failure(label, p.log, err.Error(), "services not started", ""), false
	}
	return func() {
		// Teardown gets a context of its own: the run's may already be
		// cancelled, and a cancelled teardown is the leak this exists to
		// prevent. Leaked containers poison the next local run, and the port
		// they hold is the next component's to bind.
		if err := p.stack.Down(context.WithoutCancel(ctx)); err != nil {
			fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", p.c.Name, err)
		}
	}, ui.Row{}, true
}

// runCommands runs a component's setup or teardown list, in order, stopping
// at the first failure.
//
// Through a shell, because these are free-form and repository-authored: a
// migration is `make migrate && ./seed.sh`, which argv cannot express.
func runCommands(ctx context.Context, dir, label string, c component.Component, tc *toolchain.Env, kind string, cmds []string, log *componentLog) (ui.Row, bool) {
	for _, cmd := range cmds {
		// #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- the command comes from the scanned repository's own component declaration, authored by whoever configured lydite for that repo, not from remote input
		// An empty invocation: a setup command gets the component's toolchain
		// and its declared environment, and not the pinned runner's directory.
		// It is the repository's own shell, not lydite's runner — a migration
		// or a seed script has no business finding `cargo nextest` on PATH
		// because lydite is about to run one.
		if res := executil.RunOutput(ctx, dir, childEnv(tc, c, runner.Invocation{}), log.out, "sh", "-c", cmd); !res.Ok() {
			return failure(label, log, cmd+" failed in "+c.Dir, kind+" failed", res.Output), false
		}
	}
	return ui.Row{}, true
}

// prepare puts what the runner needs in place, and reports a failing row
// rather than letting the suite start without it.
//
// A JavaScript suite run without its node_modules fails at import, naming the
// tests rather than the absent dependencies, and a Rust one without its pinned
// runner fails with `no such command` — the same misattribution a suite run
// without its database produces, and the same reason to stop first.
func prepare(ctx context.Context, inv runner.Invocation, dir, label string, c component.Component, cfg config.Config, tc *toolchain.Env, log *componentLog) (ui.Row, bool) {
	r, ok := runner.Lookup(c.Runner)
	if !ok || r.Prepare == nil {
		return ui.Row{}, true
	}
	// Two environments, because two different people's software gets
	// installed here: the repository's dependencies with what the repository
	// declared, and lydite's pinned runners with lydite's toolchain alone.
	env := executil.Env{Check: childEnv(tc, c, inv), Install: tc.Environ()}
	if err := r.Prepare(ctx, inv, dir, cfg.TypeScript.Install, env, log.out); err != nil {
		row := failure(label, log, err.Error(), "not prepared", "")
		if r.Lang == runner.TypeScript {
			row.Detail = append(row.Detail, "Set typescript.install in "+config.FileName+" if this component installs differently.")
		}
		return row, false
	}
	return ui.Row{}, true
}

// invocation is the plain variant of a component's suite: the fast path, and
// the only one this command wants. The coverage gate reads the instrumented
// variant, and mutation needs all three.
func invocation(c component.Component, variant runner.Variant) (runner.Invocation, error) {
	if len(c.Command) > 0 {
		return runner.Invocation{Name: c.Command[0], Args: c.Command[1:]}, nil
	}
	r, ok := runner.Lookup(c.Runner)
	if !ok {
		return runner.Invocation{}, fmt.Errorf("unknown runner %q", c.Runner)
	}
	inv, ok := r.Build(variant, c.Args)
	if !ok {
		return runner.Invocation{}, fmt.Errorf("runner %q supplies no %s variant", c.Runner, variant)
	}
	return inv, nil
}

// invocationFor is the invocation this run executes, which is the plain or
// instrumented variant unless the flaky gate needs a report the variant does
// not write.
//
// invocation itself is left alone, because mutation and the baseline
// measurement read it and neither asked for the gate: the plain variant is the
// bare `go test` mutation runs once per mutant, and a JUnit report written and
// discarded thousands of times is a process in the way of the thing being
// timed (ADR 0027).
func invocationFor(c component.Component, variant runner.Variant, gate *flakyGate) (runner.Invocation, error) {
	if gate.gates(c) && variant == runner.Plain {
		// The gate reads run 1's outcomes out of the report the suite wrote,
		// and under --no-coverage a plain variant writes none. Asking for the
		// gate therefore makes the run write one whichever variant it ran
		// (ADR 0039, ADR 0041).
		// cargo-llvm-cov-nextest is absent on purpose: its plain variant
		// already runs through cargo-llvm-cov, which asks for the JUnit
		// report unconditionally, so it needs no substitute here.
		junitPlain := map[runner.Name]func([]string) (runner.Invocation, bool){
			runner.GoTest:       runner.GoJUnitPlain,
			runner.CargoNextest: runner.CargoNextestJUnitPlain,
			runner.Vitest:       runner.VitestJUnitPlain,
		}
		if build, ok := junitPlain[c.Runner]; ok {
			if inv, ok := build(c.Args); ok {
				return inv, nil
			}
		}
	}
	return invocation(c, variant)
}

// flakyLabel is how every row about one component's new tests is named.
//
// Labelled the way testLabel is, and for the same reason: a component called
// `flaky` must not be able to take this gate's row, and a consumer keying rows
// by label would silently lose one of the two.
func flakyLabel(name string) string { return "flaky(" + name + ")" }

// flakyGate is the `--gate-flaky` run: what makes a test new, and what each
// component's two runs established about the tests it introduced.
//
// The merge-base and the paths the change touched are resolved once for the
// run, because they are facts about the change rather than about any
// component. Each component's verdict is filled in from the goroutine running
// that component — the rerun has to happen inside the component's own run,
// before its services come down — and every row is rendered afterwards, in
// declaration order.
type flakyGate struct {
	// requested is the flag. A gate nobody asked for still takes a row per
	// component, as context: a section that quietly disappears is
	// indistinguishable from a concern that passed.
	requested bool
	// base is the revision a test is new against, and changed the paths this
	// change touched, relative to the scan root. An empty pair is a tree with
	// no change against its base, where nothing is new.
	base    string
	changed []string
	// why names what stopped the gate before any component ran. It is one
	// unmeasured row per component rather than an error, the way --affected's
	// unresolvable merge-base is not: this gate narrows nothing and skips no
	// suite, so a run that cannot examine it still ran everything it was asked
	// to.
	why string

	mu    sync.Mutex
	rows  map[string]ui.Row
	found []finding.Finding
}

// newFlakyGate resolves what the gate needs before any component starts.
func newFlakyGate(ctx context.Context, dir, baseBranch string, requested bool) *flakyGate {
	g := &flakyGate{requested: requested, rows: map[string]ui.Row{}}
	if !requested {
		return g
	}
	base, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
	if err != nil {
		g.why = "the merge-base could not be resolved, so no test can be called new: " + err.Error()
		return g
	}
	head, err := gitstate.HeadSHA(ctx, dir)
	if err != nil {
		g.why = "HEAD could not be resolved: " + err.Error()
		return g
	}
	// A tree that is its own merge-base has no change to introduce a test, so
	// every component reports no new tests. It is left holding no paths, which
	// says exactly that and asks git for no diff at all.
	if head == base {
		return g
	}
	touched, err := gitdiff.Changed(ctx, dir, base)
	if err != nil {
		g.why = "the change against " + shortSHA(base) + " could not be read: " + err.Error()
		return g
	}
	prefix, err := gitdiff.Prefix(ctx, dir)
	if err != nil {
		g.why = "the scan root could not be located inside the repository: " + err.Error()
		return g
	}
	g.base = base
	for _, p := range touched.All {
		// The diff is repository-relative and a new test is decided over paths
		// relative to the scan root. A path outside that root declares no test
		// this run could rerun.
		if rel, inside := gitdiff.Rel(prefix, p); inside {
			g.changed = append(g.changed, rel)
		}
	}
	return g
}

// gates reports whether this component's suite has to write a JUnit report for
// the gate to read its first outcomes from.
func (g *flakyGate) gates(c component.Component) bool {
	return g != nil && g.requested && len(c.Command) == 0 && gatedRunner(c.Runner)
}

// gatedRunner is the runners the gate can examine: the four that write a
// JUnit report lydite installs nothing into the repository to obtain, and
// whose new tests a parser can enumerate statically.
//
// jest is outside it deliberately rather than by omission (ADR 0041): it ships
// no JUnit reporter, and installing jest-junit into a workspace lydite is
// about to gate is a scanner changing what the repository resolves to.
func gatedRunner(name runner.Name) bool {
	switch name {
	case runner.GoTest, runner.CargoNextest, runner.CargoLLVMCovNextest, runner.Vitest:
		return true
	}
	return false
}

// run reruns one component's new tests and records what the two runs
// established, from the goroutine running that component.
func (g *flakyGate) run(ctx context.Context, root, dir string, c component.Component, inv runner.Invocation, tc *toolchain.Env, log *componentLog) {
	if g == nil || !g.requested {
		return
	}
	row, found := g.examine(ctx, root, dir, c, inv, tc, log)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rows[c.Name] = row
	g.found = append(g.found, found...)
}

// examine is one component's verdict: which of its tests the change
// introduced, what a second run of them says, and the finding each
// disagreement makes.
func (g *flakyGate) examine(ctx context.Context, root, dir string, c component.Component, inv runner.Invocation, tc *toolchain.Env, log *componentLog) (ui.Row, []finding.Finding) {
	label := flakyLabel(c.Name)
	// What the component is comes before what the run could resolve: a Rust
	// component is outside this slice whatever the checkout looks like, and a
	// row blaming the merge-base would send its author after the wrong thing.
	switch {
	case len(c.Command) > 0:
		// A raw command opts out of the derived variants, so there is no
		// invocation to filter down to a set of test names and no report to
		// read a first outcome from.
		return unexaminedRow(label, "the component declares a raw command, which opts out of the derived variants"), nil
	case !gatedRunner(c.Runner):
		// Named rather than skipped: a repository that asked for the gate and
		// got silence in one of its languages must be able to see that it did.
		return unexaminedRow(label, flakyGap(c)), nil
	case g.why != "":
		return unexaminedRow(label, g.why), nil
	}
	identity, build := flakyRerunner(c)
	tests, err := flaky.NewTests(ctx, root, g.base, langOf(c), path.Clean(c.Dir), componentPaths(c.Dir, g.changed))
	switch {
	case errors.Is(err, flaky.ErrNoMergeBase):
		return unexaminedRow(label, err.Error()), nil
	case err != nil:
		return unexaminedRow(label, "the tests this change introduces could not be read: "+err.Error()), nil
	case len(tests) == 0:
		return ui.Row{Status: ui.StatusPass, Label: label, Value: "no new tests"}, nil
	case inv.JUnitReport == "":
		return unexaminedRow(label, "the suite wrote no test report, so no first outcome could be read"), nil
	}
	// Run 1's report is read in the same key space the rerun's will be. Two
	// runs compared across two key spaces agree about nothing.
	run1, err := identity.ReadOutcomes(filepath.Join(dir, filepath.FromSlash(inv.JUnitReport)))
	if err != nil {
		return unexaminedRow(label, "the suite's own report could not be read: "+err.Error()), nil
	}
	results, err := flaky.Rerun(ctx, tests, flaky.Options{
		Root: root,
		Dir:  c.Dir,
		Args: c.Args,
		// The rerun goes through the same pinned wrapper run 1 did, so the
		// environment that found it is the environment that finds it again.
		Env:      childEnv(tc, c, inv),
		Run1:     run1,
		Identity: identity,
		Build:    build,
		Log:      log.out,
	})
	if err != nil {
		return unexaminedRow(label, "the rerun did not finish: "+err.Error()), nil
	}
	return flakyRow(label, c, results)
}

// flakyRerunner is how one component's language addresses a test and how its
// scope's new tests become a second invocation.
//
// The build closure is what keeps internal/flaky ignorant of any runner's
// argv: the three runners filter on entirely different things — a relative
// package pattern, an OR-ed exact-name expression, a file list and a title
// regexp — and an interface per call would say the same thing in more words.
func flakyRerunner(c component.Component) (flaky.Identity, func(string, []flaky.Test) (runner.Invocation, bool)) {
	switch c.Runner {
	case runner.CargoNextest, runner.CargoLLVMCovNextest:
		// The rerun is plain cargo-nextest either way: instrumentation is a
		// runner substitution rather than a flag (ADR 0016), so a component
		// whose ordinary variant already runs through cargo-llvm-cov reruns
		// its new tests exactly as an uninstrumented one does.
		return flaky.ByClassAndName, func(_ string, tests []flaky.Test) (runner.Invocation, bool) {
			return runner.RustRerun(c.Args, flakyNames(tests))
		}
	case runner.Vitest:
		return flaky.ByClassAndName, func(_ string, tests []flaky.Test) (runner.Invocation, bool) {
			return runner.VitestRerun(c.Args, flakyFiles(tests), flakyNames(tests))
		}
	default:
		return flaky.ByName, func(scope string, tests []flaky.Test) (runner.Invocation, bool) {
			pattern, err := relPackage(c.Dir, scope)
			if err != nil {
				return runner.Invocation{}, false
			}
			return runner.GoRerun(c.Args, pattern, flakyNames(tests))
		}
	}
}

// flakyNames is the distinct names a scope's new tests carry, in order.
//
// Distinct because one name recorded under two classnames is two tests and one
// filter term: `test(=shared_name)` selects the test in every binary declaring
// it, and naming it twice would only make the expression longer.
func flakyNames(tests []flaky.Test) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tests {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t.Name)
	}
	return out
}

// flakyFiles is the distinct files a scope's new tests were reported in, named
// the way the directory the rerun runs in names them.
//
// Taken from the classname, which for vitest is the file's own path relative
// to the component — the same shape a positional argument is resolved in,
// since the rerun runs in the component's directory. A test's declaration site
// is not the same question: a title declared in a helper the test file imports
// is reported under the file vitest ran, and it is that file the rerun has to
// name.
func flakyFiles(tests []flaky.Test) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tests {
		if t.Classname == "" || seen[t.Classname] {
			continue
		}
		seen[t.Classname] = true
		out = append(out, t.Classname)
	}
	return out
}

// relPackage turns a package directory relative to the scan root into the
// pattern `go test` takes in the component's own directory.
//
// A pattern and not an import path, because deriving one would need `go list`
// over a module the gate has not otherwise had to load, and a relative pattern
// names the same package for a component whose module path lydite never reads.
// It is spelled with a leading "./" for the reason cmd/go requires one: a bare
// `pkg` is a path in the module cache, and only `./pkg` is a directory here.
func relPackage(dir, pkg string) (string, error) {
	rel, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(pkg))
	if err != nil {
		return "", fmt.Errorf("locating %s inside the component at %s: %w", pkg, dir, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") {
		return rel, nil
	}
	return "./" + path.Clean(rel), nil
}

// report adds one row per component this run was responsible for, in
// declaration order, and every finding the disagreements made.
//
// A component whose suite never ran still takes a row. Its new tests were not
// examined, and a gate that could not run never renders as one that passed.
func (g *flakyGate) report(rep *ui.Report, own []component.Component) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range own {
		switch row, ok := g.rows[c.Name]; {
		case ok:
			rep.Add(row)
		case !g.requested:
			rep.Add(ui.Row{Status: ui.StatusContext, Label: flakyLabel(c.Name),
				Value: "not gated — --gate-flaky reruns the tests a change introduces"})
		default:
			rep.Add(unexaminedRow(flakyLabel(c.Name), "the component's suite did not run, so there was nothing to rerun"))
		}
	}
	rep.AddFindings(g.found...)
}

// flakyRow is what one component's results establish, and the finding each
// disagreement makes.
//
// The strongest verdict owns the row and the weaker one is still said: a run
// holding both a disagreement and a test nothing could examine fails, with the
// unexamined count in the detail.
func flakyRow(label string, c component.Component, results []flaky.Result) (ui.Row, []finding.Finding) {
	var disagreed, unexamined, skipped int
	var detail []string
	var found []finding.Finding
	for _, r := range results {
		switch r.Verdict {
		case flaky.Disagreed:
			disagreed++
			detail = append(detail,
				fmt.Sprintf("%s: run 1 %s, run 2 %s", flakyName(r.Test), outcomeOf(r.Run1), outcomeOf(r.Run2)),
				"rerun in "+c.Dir+": "+r.Command)
			found = append(found, flakyFinding(label, c, r))
		case flaky.Unmeasured:
			unexamined++
			detail = append(detail, flakyName(r.Test)+" was not examined: "+r.Why)
		case flaky.Skipped:
			// Both runs skipped it, which agrees and examined nothing. It
			// counts toward "could not be measured" alongside Unmeasured,
			// never toward a pass: a component whose new tests all call
			// t.Skip() has run nothing twice, and rendering that as "2 runs
			// each" is exactly the gate-that-could-not-run-as-a-pass failure
			// this gate exists to refuse.
			skipped++
			detail = append(detail, flakyName(r.Test)+" was skipped in both runs")
		case flaky.Agreed:
		}
	}
	// No finding.Number pass: Site is the scope, the classname where the
	// language has one, and the test's name — which the report's own keys
	// already guarantee distinct, so two disagreements from one component can
	// never share a (Path, Site) pair for an ordinal to disambiguate. Ordinal
	// stays its zero value.
	unmeasured := unexamined + skipped
	row := ui.Row{Label: label, Detail: detail}
	switch {
	case disagreed > 0:
		row.Status = ui.StatusFail
		row.Value = fmt.Sprintf("%d of %d new test(s) disagreed between two runs", disagreed, len(results))
	case unmeasured == len(results):
		row.Status = ui.StatusUnmeasured
		row.Value = fmt.Sprintf("not examined — none of the %d new test(s) could be measured", len(results))
	case unmeasured > 0:
		// Some agreed and none disagreed, but a test this run could not run
		// twice — whether because nothing recorded it or because it skipped
		// both times — is not one it can call agreeing either: a pass here
		// would count a test that was never actually rerun among the ones
		// that were, and a gate that examined part of the change must not
		// render as one that examined all of it.
		row.Status = ui.StatusUnmeasured
		row.Value = fmt.Sprintf("%d of %d new test(s) could not be measured", unmeasured, len(results))
	default:
		row.Status = ui.StatusPass
		row.Value = fmt.Sprintf("%d new test(s), 2 runs each", len(results))
	}
	return row, found
}

// flakyFinding is the claim one disagreement makes, on the line the test is
// declared at.
//
// The site is the test's identity — the scope, the classname where a name
// alone is not one, and the name — so reformatting the file above the
// declaration does not re-identify the claim. The ordinal is zero because that
// triple is unique within a component: a name is unique in a Go package by the
// compiler's own rule, and elsewhere the classname is what tells two
// same-named tests apart.
func flakyFinding(label string, c component.Component, r flaky.Result) finding.Finding {
	return finding.Finding{
		Gate:      "flaky",
		Component: c.Name,
		Row:       label,
		Path:      r.Test.Path,
		Line:      r.Test.Line,
		Message:   flakyName(r.Test) + " disagreed between two runs",
		Detail: []string{
			"run 1: " + outcomeOf(r.Run1) + ", run 2: " + outcomeOf(r.Run2),
			"The rerun ran it alone, in " + c.Dir + ": " + r.Command,
			"Two runs disagreed. That is not a claim the test is random — a test that only passes because another test ran first disagrees here too, and is non-deterministic in the sense that matters.",
		},
		Site: r.Test.Scope + " " + flakyName(r.Test),
	}
}

// flakyName is how a row and a finding call one test.
//
// The classname comes first where there is one, because it is what tells two
// same-named tests in one component apart — `nextestprobe::a shared_name` and
// `nextestprobe::b shared_name` are two tests, and a row naming both
// `shared_name` says one thing twice. A declaration no parser could name is
// called by the only thing that identifies it: where it sits.
func flakyName(t flaky.Test) string {
	switch {
	case t.Unreadable:
		return fmt.Sprintf("the test declared at %s:%d", t.Path, t.Line)
	case t.Classname != "":
		return t.Classname + " " + t.Name
	}
	return t.Name
}

// outcomeOf names an outcome a report may not have recorded at all.
func outcomeOf(o *junit.Outcome) string {
	if o == nil {
		return "absent"
	}
	return o.String()
}

// flakyGap says why a component's runner is one the gate cannot examine.
//
// jest has a reason of its own rather than the general one, because it is a
// decision and not unshipped scope: it ships no JUnit reporter, and the only
// implementation is the one ADR 0029 refuses — installing jest-junit into the
// workspace lydite is about to gate.
func flakyGap(c component.Component) string {
	if c.Runner == runner.Jest {
		return "jest has no JUnit output lydite will install"
	}
	if langOf(c) != "" {
		return "the flaky gate has no second run for a " + string(c.Runner) + " suite"
	}
	return "this component declares no runner lydite knows"
}

// componentPaths is the changed paths that lie inside one component, which is
// the whole of what its own gate may look at.
//
// A component's row is about the tests it introduced, and flaky.NewTests reads
// whatever paths it is given: handed the whole diff, every component would
// rerun every other component's new tests and report them under its own name.
func componentPaths(dir string, changed []string) []string {
	dir = path.Clean(dir)
	if dir == "." {
		return changed
	}
	var out []string
	for _, p := range changed {
		if strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	return out
}

// unexaminedRow is a gate that examined nothing, with the cause beside it.
func unexaminedRow(label, why string) ui.Row {
	return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "not examined — " + why}
}

// childEnv is the environment one of a component's commands runs with: the
// invocation's own pinned-tool directories ahead of the component's resolved
// toolchain on PATH, then the component's declared variables, and the
// toolchain's last of all.
//
// One function, and one PATH entry, because a child's environment is a flat
// list where the last occurrence of a key wins — two callers each prepending
// their own directories produce two PATH entries and one of them is silently
// dropped, which is invisible in argv and in every log. The pinned tool goes
// first on PATH because it is the more specific of the two.
//
// **The toolchain's variables go last, so they win**, and the ordering is the
// whole of the rule: a component declaring `GOTOOLCHAIN: auto` would otherwise
// cancel the `GOTOOLCHAIN=local` pinAmbientGo exists to set, reinstating the
// `go install` downgrade that made govulncheck reject the source it was
// pointed at — with every run still green. A repository states how its own
// code builds; lydite states which toolchain builds it. Reordering these
// arguments is that regression, so the inline comment at the return says it
// again where the change would be made.
//
// A PATH the component declares is folded into that one entry rather than set
// as a variable of its own — it is the single key a component cannot simply
// state, because the composed entry would always be the later of the two and
// would win outright.
//
// It is appended **after** the inherited PATH, and that is the security
// boundary rather than a preference. lydite resolves a program against the
// environment it hands the child, so a declared directory placed ahead of the
// inherited one would let `.lydite/components.yml` decide which `go`, `cargo`,
// `npm` or `sh` lydite itself launches: a repository shipping `ci-bin/go` and
// declaring `env: {PATH: ci-bin}` would have `lydite scan` run that binary to
// install gosec, on a runner where the ambient toolchain was already verified.
// A component may extend the path its suite runs with; it may not choose the
// toolchain lydite runs. Ordering keeps the useful case — a helper that exists
// nowhere else is still found — and removes the shadowing one.
func childEnv(tc *toolchain.Env, c component.Component, inv runner.Invocation) []string {
	dirs := append([]string{}, inv.PathDirs...)
	if tc != nil {
		dirs = append(dirs, tc.PathDirs...)
	}
	declared, vars := splitPath(env(c))
	if tc == nil {
		return toolchain.Compose(dirs, declared, vars)
	}
	// tc.Vars last, so they win: see the ordering rule above. Swapping these
	// two lets a declared GOTOOLCHAIN cancel the pin, and nothing goes red.
	return toolchain.Compose(dirs, declared, vars, tc.Vars)
}

// splitPath separates a PATH a component declared into its directories,
// returning the remaining variables untouched. The last PATH wins, matching
// how a process reads duplicate keys out of its own environment.
func splitPath(declared []string) (dirs, vars []string) {
	path := ""
	for _, kv := range declared {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
			continue
		}
		vars = append(vars, kv)
	}
	if path == "" {
		return nil, vars
	}
	return filepath.SplitList(path), vars
}

// env renders a component's declared environment as the "KEY=value" entries
// executil appends to the child's own.
func env(c component.Component) []string {
	if len(c.Env) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Env))
	for k, v := range c.Env {
		out = append(out, k+"="+v)
	}
	// Sorted, so two runs of the same declaration hand the child the same
	// environment in the same order: a map's iteration order is not one a
	// failure can be reproduced from.
	sort.Strings(out)
	return out
}

// componentUnits is what each of these components needs a toolchain for, in
// declaration order.
//
// The caller says which components, and it is the set that run may execute
// rather than the set affected selection ended up choosing: a component
// deselected on one run and selected on the next must not resolve a different
// toolchain, and selection is not known until after the git walk this feeds.
// A shard's set is its own, since it can never execute a component outside it
// and materialising a rustup channel for one is a download nothing uses.
//
// A component declaring its own command implies no language and needs nothing.
func componentUnits(components []component.Component) []toolchain.Unit {
	var out []toolchain.Unit
	for _, c := range components {
		lang := langOf(c)
		if lang == "" {
			continue
		}
		out = append(out, toolchain.Unit{Name: c.Name, Lang: lang, Dir: c.Dir})
	}
	return out
}

// orphanRow runs the orphan gate and renders its verdict.
//
// It fails, rather than referring: a source file under no component is
// something the author clears by doing work they can do — declaring the
// component, or writing the exclude that says this code is tested by nobody
// and someone decided that. Both leave a line in a file whose history is the
// record of what gets tested.
//
// A tree that is not a git repository reports unmeasured and passes. The gate
// is preparation for nothing and blocks nobody in that state, and turning a
// working `lydite test` in an exported tarball into a hard failure would be
// the gate firing on ordinary work. Distinct from a pass, because a gate that
// did not run must never read as one.
func orphanRow(ctx context.Context, dir string, file component.File) ui.Row {
	const label = "orphans"
	res, err := orphan.Find(ctx, dir, file)
	if errors.Is(err, orphan.ErrNoRepository) {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no git repository"}
	}
	if errors.Is(err, orphan.ErrNoFiles) {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no source files found"}
	}
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not checked", Detail: []string{err.Error()}}
	}
	for _, e := range res.UnusedExcludes {
		// The rule, not a guess at what was meant. Deriving a suggestion from
		// the pattern produces advice that cannot be followed as soon as the
		// pattern is not a bare directory name: "tools/gen.go" becomes
		// "tools/gen.go/**", and a stale exclude whose file was deleted —
		// the other reason one covers nothing — has no better spelling at all.
		fmt.Fprintf(os.Stderr, "lydite: %s: exclude %q covers no file. Patterns are anchored, so a subtree is spelled \"dir/**\"\n", component.FileName, e)
	}
	if len(res.Orphans) == 0 {
		return ui.Row{Status: ui.StatusPass, Label: label, Value: fmt.Sprintf("none in %d source file(s)", res.Scanned)}
	}
	// Every orphan, not a sample. The author's next action is to decide
	// which component each one belongs to, and a truncated list turns that
	// into a second run to discover the rest.
	detail := make([]string, 0, len(res.Orphans)+1)
	detail = append(detail, res.Orphans...)
	detail = append(detail, "declare a component covering these, or add them to "+component.FileName+"'s excludes")
	return ui.Row{
		Status: ui.StatusFail,
		Label:  label,
		Value:  fmt.Sprintf("%d under no component", len(res.Orphans)),
		Detail: detail,
	}
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
	// On the default branch the merge-base is HEAD itself, so there is no
	// change to select by and a computed selection narrows to nothing. ADR
	// 0016 requires that run to be complete — a forgotten depends_on edge
	// surfaces at merge or never — so this is where that rule is enforced
	// rather than left to every caller to remember. Without it a consumer
	// wiring --affected into one workflow gets a permanently green `lydite
	// test` that executed no suite at all.
	head, err := gitstate.HeadSHA(ctx, dir)
	if err != nil {
		return affected.Result{}, err
	}
	if head == base {
		return affected.All(file, affected.Reason{Kind: affected.KindDefaultBranch}), nil
	}
	touched, err := gitdiff.Changed(ctx, dir, base)
	if err != nil {
		return affected.Result{}, err
	}
	// Component directories are scan-root relative and the diff is
	// repository-root relative, so the two are mapped before anything is
	// matched. A path outside the scan root matches no component and
	// therefore widens.
	prefix, err := gitdiff.Prefix(ctx, dir)
	if err != nil {
		return affected.Result{}, err
	}
	return affected.Select(file, affected.Paths(prefix, touched.All)), nil
}

// selectRow says what selection actually did.
//
// It carries the count because "0 of 4 affected" and "4 of 4 passed" must not
// read alike, and the reason each selected component was chosen because a
// selection that quietly returned everything is otherwise indistinguishable
// from one that narrowed correctly.
//
// Zero selected is unmeasured rather than a pass. It can only mean the diff
// was empty — every changed path selects at least one component — so nothing
// was gated, and a gate that did not run must never render as one that did.
func selectRow(res affected.Result, declared int) ui.Row {
	row := ui.Row{
		Status: ui.StatusPass,
		Label:  "select",
		Value:  fmt.Sprintf("%d of %d affected", len(res.Selected), declared),
	}
	if len(res.Selected) == 0 {
		row.Status = ui.StatusUnmeasured
		row.Detail = []string{"no changes against the merge-base, so no component could have been broken"}
		return row
	}
	// A reason about the run rather than about any component is stated once.
	// Repeating "every component runs on the default branch" per component
	// scales with the declaration and says nothing more at the twentieth
	// line than at the first.
	if r := res.Reasons[res.Selected[0].Name]; r.Kind == affected.KindDefaultBranch {
		row.Detail = []string{r.String()}
		return row
	}
	for _, c := range res.Selected {
		row.Detail = append(row.Detail, c.Name+": "+res.Reasons[c.Name].String())
	}
	return row
}

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

// intersect narrows a selection to the components this run is responsible
// for, keeping the order it was given.
//
// Selection runs over the whole declaration, because a dependency edge
// reaches components in other shards and a closure computed over a slice
// would stop at its boundary. What each shard then reports is its own share
// of that one answer.
func intersect(cs []component.Component, own []component.Component) []component.Component {
	mine := make(map[string]bool, len(own))
	for _, c := range own {
		mine[c.Name] = true
	}
	out := make([]component.Component, 0, len(cs))
	for _, c := range cs {
		if mine[c.Name] {
			out = append(out, c)
		}
	}
	return out
}
