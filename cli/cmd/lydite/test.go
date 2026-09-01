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
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/ui"
)

func newTestCmd() *cobra.Command {
	var dir string
	var components []string
	var asJSON, noColor, stream, onlyAffected bool
	var concurrency string
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

			selected, err := file.Select(components)
			if err != nil {
				return err
			}
			if onlyAffected {
				if len(components) > 0 {
					// Two narrowing mechanisms composed. The planner emits
					// per-shard --component lists that are already the
					// selected set, so the combination is never needed — and
					// an intersection nobody asked for narrows silently when
					// it is wrong.
					return errors.New("--affected and --component both narrow the run; pass one")
				}
				var res affected.Result
				res, err = selectAffected(ctx, dir, file)
				if err != nil {
					return err
				}
				selected = res.Selected
				rep.Add(selectRow(res, len(file.Components)))
				// The same label shape a suite row takes, so every
				// component produces exactly one test(<name>) row whether it
				// ran or not. A bare name would collide with a gate row for
				// a component called "watch", "select", "orphans" or
				// "schedule" — nothing forbids those — and a consumer keying
				// rows by label silently loses the gate.
				for _, c := range res.Skipped {
					rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: "test(" + c.Name + ")", Value: "not affected"})
				}
			}
			if len(selected) == 0 {
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
				return renderTestReport(cmd, rep, asJSON, noColor)
			}

			runComponents(ctx, dir, selected, cfg, limit, stream, rep)
			return renderTestReport(cmd, rep, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&components, "component", nil, "component to run; repeatable, and every declared component by default")
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
		"run only the components the change against the merge-base could have broken")
	// Every run captures; this only adds the terminal. A suite that hangs
	// prints nothing until it is killed, and its log is written but not yet
	// interesting — watching it is the case a captured file cannot serve.
	cmd.Flags().BoolVar(&stream, "stream", false, "mirror each component's output to stderr as it runs, as well as to its log")
	return cmd
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
// "max" is a slot for every component, which is what the planner passes when it
// wants a shard to hold everything it was given; the scheduler never admits
// more than it has, so the bound needs no count to be resolved against and can
// be checked before any work happens. A number below one is refused rather than
// clamped: it is a typo, and silently running anyway would have lydite ignore
// something the caller said.
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
func runComponents(ctx context.Context, root string, selected []component.Component, cfg config.Config, limit int, stream bool, rep *ui.Report) {
	plans := planComponents(ctx, root, selected, stream)
	for _, p := range plans {
		defer p.log.Close()
	}

	rows := make([]ui.Row, len(plans))
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
			Label:  "test(" + p.c.Name + ")",
			Value:  "not run",
			Detail: []string{"the run ended before this component started"},
		}
		items = append(items, itemFor(p))
		index = append(index, i)
	}

	outcome := scheduler.Run(ctx, items, limit, func(ctx context.Context, k int) {
		i := index[k]
		rows[i] = runComponent(ctx, root, plans[i], cfg)
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
		}
	}

	rep.Add(scheduleRow(ctx, outcome, len(plans), limit))
	for _, r := range rows {
		rep.Add(r)
	}
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
func planComponents(ctx context.Context, root string, selected []component.Component, stream bool) []componentPlan {
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
		p := componentPlan{c: c, log: openLog(root, c.Name, stream, width), ready: true}
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
					Label:  "test(" + c.Name + ")",
					Value:  "not run",
					Detail: []string{"the run ended before this component started"},
				}
			} else {
				p.row = failure("test("+c.Name+")", p.log, err.Error(), "services not started", "")
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
func runComponent(ctx context.Context, root string, p componentPlan, cfg config.Config) (row ui.Row) {
	c, log := p.c, p.log
	label := "test(" + c.Name + ")"

	inv, err := invocation(c)
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}
	}

	dir := filepath.Join(root, filepath.FromSlash(c.Dir))
	if prepared, ok := prepare(ctx, dir, label, c, cfg, log); !ok {
		return prepared
	}

	// Deferred before anything starts, so every path out of here tears the
	// stack down — including a setup that failed halfway, which is when a
	// half-applied migration most needs undoing.
	stop, started, ok := startServices(ctx, p, label)
	if !ok {
		return started
	}
	defer stop()
	defer func() {
		// Teardown gets a context of its own, because the run's may already
		// be cancelled and a cancelled teardown is the leak this prevents.
		failed, ok := runCommands(context.WithoutCancel(ctx), dir, label, c, "teardown", c.Teardown, log)
		// A failing teardown turns a passing component into a failing one —
		// it has left state behind that the next run will inherit — but it
		// never masks a failure that already happened, because the earlier
		// one is what the reader has to act on.
		if !ok && row.Status == ui.StatusPass {
			row = failed
		}
	}()

	if failed, ok := runCommands(ctx, dir, label, c, "setup", c.Setup, log); !ok {
		return failed
	}

	if res := executil.RunOutput(ctx, dir, append(env(c), inv.Env...), log.out, inv.Name, inv.Args...); !res.Ok() {
		return failure(label, log, strings.Join(append([]string{inv.Name}, inv.Args...), " ")+" in "+c.Dir, "failed", res.Output)
	}
	return ui.Row{Status: ui.StatusPass, Label: label, Value: "passed", Log: log.Rel}
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

// openLog creates the component's log. A log that cannot be created is not a
// reason to skip the component, so it degrades to capture-only: the tail under
// a failing row still names the cause, which is most of what the file is for.
func openLog(root, name string, stream bool, width int) *componentLog {
	l := &componentLog{out: io.Discard, name: name, width: width}
	dir := filepath.Join(root, runner.ReportDir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", name, err)
		return l.streamed(stream)
	}
	path := filepath.Join(dir, "test.log")
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
func runCommands(ctx context.Context, dir, label string, c component.Component, kind string, cmds []string, log *componentLog) (ui.Row, bool) {
	for _, cmd := range cmds {
		// #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- the command comes from the scanned repository's own component declaration, authored by whoever configured lydite for that repo, not from remote input
		if res := executil.RunOutput(ctx, dir, env(c), log.out, "sh", "-c", cmd); !res.Ok() {
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
func prepare(ctx context.Context, dir, label string, c component.Component, cfg config.Config, log *componentLog) (ui.Row, bool) {
	r, ok := runner.Lookup(c.Runner)
	if !ok || r.Prepare == nil {
		return ui.Row{}, true
	}
	if err := r.Prepare(ctx, dir, cfg.TypeScript.Install, log.out); err != nil {
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
func invocation(c component.Component) (runner.Invocation, error) {
	if len(c.Command) > 0 {
		return runner.Invocation{Name: c.Command[0], Args: c.Command[1:]}, nil
	}
	r, ok := runner.Lookup(c.Runner)
	if !ok {
		return runner.Invocation{}, fmt.Errorf("unknown runner %q", c.Runner)
	}
	inv, ok := r.Build(runner.Plain, c.Args)
	if !ok {
		return runner.Invocation{}, fmt.Errorf("runner %q supplies no plain variant", c.Runner)
	}
	return inv, nil
}

// env renders a component's declared environment as the "KEY=value" entries
// executil appends to the child's own.
//
// An invocation's own Env is appended after this, so a pinned tool's PATH wins
// over a component's: the component says what its suite needs, and lydite says
// which build of the runner runs it.
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

// renderTestReport renders and returns the verdict as an exit code.
func renderTestReport(cmd *cobra.Command, rep *ui.Report, asJSON, noColor bool) error {
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
func selectAffected(ctx context.Context, dir string, file component.File) (affected.Result, error) {
	base, err := gitstate.BaseSHA(ctx, dir)
	if err != nil {
		return affected.Result{}, fmt.Errorf("--affected needs the merge-base with the default branch, and it could not be resolved: %w"+
			"\n       a shallow checkout is the usual cause; fetch with depth 0", err)
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
