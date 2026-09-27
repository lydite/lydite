// Package run is the engine `lydite test` executes component suites with:
// planning each selected component, scheduling them under the port and
// directory locks they declare, preparing, starting services, running and
// tearing down each one, and the flaky gate that reruns the tests a change
// introduced from inside that same run.
//
// It owns no log. Every function that writes a component's output takes an
// Output — a writer and the path a report names for it — so whoever opens the
// log decides where it goes and when it closes, and the same engine serves a
// command whose logs are files, a mirror to the terminal, or nothing at all.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/test/measure"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// TestLabel is how every row about one component's suite is named.
//
// Labelled rather than bare, so every component produces exactly one
// test(<name>) row whether it ran or not, and so a component called `watch`,
// `select`, `orphans` or `schedule` cannot take a gate row's label. Nothing
// forbids those names, and a consumer keying rows by label would silently lose
// the gate.
func TestLabel(name string) string { return "test(" + name + ")" }

// DeclaresNoSuite reports whether a component names neither a runner nor a
// command — the shape that states only the language it is scanned as.
//
// Such a component is not a work item. It runs nothing, so it takes no
// toolchain, no services and no scheduler lock — a lock on its directory would
// serialise the components rooted beside it for nothing — and `test plan`
// places it in no shard. Every row it takes on the test side is produced from
// the declaration rather than from a run: by `lydite test` when unsharded, and
// by `test merge` when folding shards, none of which ran it.
func DeclaresNoSuite(c component.Component) bool { return c.Runner == "" && len(c.Command) == 0 }

// NoSuiteReason is why every test-side gate is unmeasured for a component that
// declares no suite. It names the declaration an author would change — adding a
// runner or a command — which is what sets it apart from a raw command's
// reason, whose author would change the command to a runner.
const NoSuiteReason = "the component declares no suite — neither a runner nor a command, only the lang: it is scanned as"

// NoSuiteTestRow is the test row of a component that declares no suite, under
// whichever command's label kind names.
func NoSuiteTestRow(kind, name string) ui.Row {
	return ui.Row{Status: ui.StatusUnmeasured, Label: kind + "(" + name + ")", Value: "not run — " + NoSuiteReason}
}

// NoSuiteCoverageRows is the coverage, complexity and — when a floor is
// configured — floor rows of a component that declares no suite, in the order
// a report shows them.
//
// Unmeasured, and never the context a raw command's complexity row takes: a
// raw command runs a suite over source whose language lydite cannot name,
// while this component states its language and runs nothing, so adding a suite
// is exactly what would make every one of these rows measurable.
func NoSuiteCoverageRows(name string, floor float64) []ui.Row {
	rows := []ui.Row{
		measure.UnmeasuredRow("coverage("+name+")", NoSuiteReason),
		measure.UnmeasuredRow("crap("+name+")", NoSuiteReason),
	}
	if floor > 0 {
		rows = append(rows, measure.UnmeasuredRow("floor("+name+")", fmt.Sprintf("the %.1f%% floor cannot apply: %s", floor, NoSuiteReason)))
	}
	return rows
}

// Output is where one component's commands write, and the path a report names
// for it.
//
// W receives everything the component's commands print. Rel is that output's
// location relative to the scan root, which is what a report shows — an
// absolute path from someone else's runner is noise in a PR comment — and is
// empty when the output lands nowhere a reader could open.
type Output struct {
	W   io.Writer
	Rel string
}

// Opener gives a component the Output its commands write to. width is the
// longest selected name, for a caller that aligns a prefix across components;
// runs is false for a component that declares no suite, whose output nothing
// will ever write.
type Opener func(c component.Component, width int, runs bool) Output

// Plan is one selected component with everything resolved that has to be
// known before anything starts.
//
// The stack is loaded here rather than inside the run because a stack is the
// only thing that knows which host ports its component publishes, and the
// scheduler needs every component's before it can decide which of them may run
// together. Loading up front also moves compose's own validation — a
// compose.up naming a service the file does not declare, a wait: healthy with
// no healthcheck — ahead of the first container, where it costs nothing.
type Plan struct {
	C     component.Component
	Out   Output
	Stack *compose.Stack // nil when the component declares no services
	Ports []int
	// Row is already final when the component cannot be run at all, and the
	// scheduler is never given it.
	Row   ui.Row
	Ready bool
}

// PlanComponents opens each component's output and loads the stack of any that
// declares services. A component declaring no suite is planned unrunnable, its
// row already final, under every command that plans through here.
//
// The container runtime is probed at most once for the whole run, and only
// when something actually declares services: a component that declares none
// needs no runtime, so a repository without services still runs on a machine
// with no container engine at all.
func PlanComponents(ctx context.Context, root string, selected []component.Component, kind string, open Opener) []Plan {
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

	plans := make([]Plan, len(selected))
	for i, c := range selected {
		// Final before anything runs, and never runnable: the scheduler is
		// never given it, so it takes no lock on its directory. No log is
		// opened either — an empty file under its name would read as the
		// output of a run that never happened.
		if DeclaresNoSuite(c) {
			plans[i] = Plan{C: c, Out: open(c, width, false), Row: NoSuiteTestRow(kind, c.Name)}
			continue
		}
		p := Plan{C: c, Out: open(c, width, true), Ready: true}
		if !c.Compose.Declared() {
			plans[i] = p
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(c.Dir))
		stack, err := compose.LoadWith(runtime, dir, c, p.Out.W)
		if err != nil {
			// A probe runs candidate `docker compose version` invocations
			// under the run's context, so an interrupt kills every one of
			// them and looks exactly like a machine with no container
			// runtime. Reporting that would be a confident wrong answer
			// about the machine, and would report an interrupted run as a
			// component that failed — the plan stays runnable instead, and
			// the scheduler starts nothing on a cancelled context, so it
			// lands on the `not run` row where it belongs.
			p.Ready = false
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
				p.Row = ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  kind + "(" + c.Name + ")",
					Value:  "not run",
					Detail: []string{"the run ended before this component started"},
				}
			} else {
				p.Row = Failure(kind+"("+c.Name+")", p.Out.Rel, err.Error(), "services not started", "")
			}
			plans[i] = p
			continue
		}
		p.Stack, p.Ports = stack, stack.HostPorts()
		plans[i] = p
	}
	return plans
}

// ItemFor is what the scheduler locks on: the component's root, cleaned so
// `./web`, `web/` and `web` are one directory, the further paths it declares
// it writes into, and the host ports its services publish.
//
// The occupied paths are cleaned by component.Parse, since a declaration the
// scheduler compares against another's is only ever read through it.
//
// One construction, because a second one that agreed today would come apart
// the day the normalisation changed — and the test built on the copy would
// keep passing.
func ItemFor(p Plan) scheduler.Item {
	return scheduler.Item{Name: p.C.Name, Dir: path.Clean(p.C.Dir), Occupies: p.C.Occupies, Ports: p.Ports}
}

// RunComponents runs every planned component under the scheduler and returns
// the rows to report, in declaration order, with one measurement per plan.
//
// Declaration order and never completion order: a reader diffing two runs
// depends on it, and ordering by whichever finished first would put this run's
// timing into the document, so two runs of the same declaration would produce
// different reports.
//
// ordered and skipped are selection's: skipped holds the row of every
// component selection did not run, keyed by name, and ordered is the
// declaration those rows interleave into. Both are empty when selection did
// not run.
//
// An interrupted run fails through the `schedule` row rather than through a
// returned error. The unstarted rows are `unmeasured`, which does not vote, so
// a run cut short by a CI job timeout would otherwise carry no failing row —
// and `--json` would publish `"verdict": "pass"` for a run that tested half the
// repository. Anything automated reads that document and never the terminal, so
// a truncation visible only in the process exit code is a truncation the PR
// comment renders green. `ui.Report.ExitCode` stays the single place the
// mapping lives.
//
// gate is the flaky gate a caller asked for, which each component's own run
// carries out before its services are torn down. A nil gate is a run nobody
// asked one of.
func RunComponents(ctx context.Context, root string, plans []Plan, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, limit int, instrument bool, gate *FlakyGate) ([]ui.Row, []measure.Measurement) {
	rows := make([]ui.Row, len(plans))
	// notes[i] is the row about the component's install, present when there
	// is something to say about it: an override and where it runs, or no
	// lockfile to install from at all. Resolved here, before anything runs,
	// because it is a question about the tree: which lockfile — or which
	// override — declares this component's dependencies, and where, which
	// no install of it changes.
	notes := make([]ui.Row, len(plans))
	measured := make([]measure.Measurement, len(plans))
	for i, p := range plans {
		measured[i] = measure.UnmeasuredComponent(p.C, "the component did not run")
	}
	var items []scheduler.Item
	var index []int
	suites := 0
	for i, p := range plans {
		if DeclaresNoSuite(p.C) {
			// Unmeasurable rather than unmeasured: nothing could ever measure
			// it, so its absence from a baseline is permanent and expected
			// rather than a gap this run left — the way a raw command's is.
			rows[i] = p.Row
			measured[i] = measure.UnmeasurableComponent(p.C, NoSuiteReason)
			continue
		}
		suites++
		if !p.Ready {
			rows[i] = p.Row
			continue
		}
		if note, ok := installNote(root, p.C, cfg); ok {
			notes[i] = note
		}
		// Pre-filled, so a component the run never reached reports that it
		// did not run rather than being dropped. A truncated run that simply
		// omitted rows would read as a complete run over fewer components,
		// and a check that could not run must never read as one that did.
		rows[i] = ui.Row{
			Status: ui.StatusUnmeasured,
			Label:  TestLabel(p.C.Name),
			Value:  "not run",
			Detail: []string{"the run ended before this component started"},
		}
		items = append(items, ItemFor(p))
		index = append(index, i)
	}

	outcome := scheduler.Run(ctx, items, limit, func(ctx context.Context, k int) {
		i := index[k]
		rows[i], measured[i] = runComponent(ctx, root, plans[i], cfg, envs.For(plans[i].C.Name), instrument, gate)
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
			measured[i] = measure.UnmeasuredComponent(plans[i].C, "the run was interrupted before this component finished")
		}
	}

	// Only a component that passed contributes a measurement. A report
	// written by a suite that failed, was killed, or never started describes
	// an unfinished run, and recording it would put a number in the baseline
	// that nothing can be compared against honestly. Enforced here, over the
	// final rows, so no path out of runComponent can forget it. A component
	// declaring no suite never reached runComponent, and keeps the
	// unmeasurable measurement its declaration gave it.
	for i := range plans {
		if rows[i].Status != ui.StatusPass && !DeclaresNoSuite(plans[i].C) {
			// The row's own words, so the coverage row and the suite row give
			// a reader the same account of one event rather than two.
			replaced := measure.UnmeasuredComponent(plans[i].C, rows[i].Label+" did not pass: "+rows[i].Value)
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

	// Counted over the components that declare a suite: one that declares none
	// was never the scheduler's to start, so it is neither a component the
	// run started nor one it failed to.
	out := []ui.Row{ScheduleRow(ctx, outcome, suites, limit)}
	// A component's install row sits immediately above the component it is
	// about, so the two are read together rather than as two reports of
	// different things.
	emit := func(i int) {
		if notes[i].Label != "" {
			out = append(out, notes[i])
		}
		out = append(out, rows[i])
	}
	if len(skipped) == 0 {
		for i := range rows {
			emit(i)
		}
		return out, measured
	}
	// Interleaved by the declaration, so a component selection skipped sits
	// where its author wrote it rather than ahead of every component that
	// ran. Two runs over one declaration already produce the same document;
	// this is what makes that document read in the order the file does.
	byName := make(map[string]int, len(rows))
	for i, r := range rows {
		byName[r.Label] = i
	}
	for _, c := range ordered {
		if r, ok := skipped[c.Name]; ok {
			out = append(out, r)
			continue
		}
		if i, ok := byName[TestLabel(c.Name)]; ok {
			emit(i)
		}
	}
	return out, measured
}

// ScheduleRow says what the scheduler actually did.
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
func ScheduleRow(ctx context.Context, outcome scheduler.Outcome, components, limit int) ui.Row {
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
func runComponent(ctx context.Context, root string, p Plan, cfg config.Config, tc *toolchain.Env, instrument bool, gate *FlakyGate) (row ui.Row, m measure.Measurement) {
	c, out := p.C, p.Out
	label := TestLabel(c.Name)
	m = measure.UnmeasuredComponent(c, "the component did not run")

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
		if err := ClearReport(dir, report); err != nil {
			return Failure(label, out.Rel, err.Error(), "not runnable", ""), m
		}
	}
	if prepared, ok := Prepare(ctx, inv, dir, root, label, c, cfg, tc, out); !ok {
		return prepared, m
	}

	// Deferred before anything starts, so every path out of here tears the
	// stack down — including a setup that failed halfway, which is when a
	// half-applied migration most needs undoing.
	stop, started, ok := StartServices(ctx, p, label)
	if !ok {
		return started, m
	}
	defer stop()
	defer func() {
		// Teardown gets a context of its own, because the run's may already
		// be cancelled and a cancelled teardown is the leak this prevents.
		failed, ok := RunCommands(context.WithoutCancel(ctx), dir, label, c, tc, "teardown", c.Teardown, out)
		// A failing teardown turns a passing component into a failing one —
		// it has left state behind that the next run will inherit — but it
		// never masks a failure that already happened, because the earlier
		// one is what the reader has to act on.
		if !ok && row.Status == ui.StatusPass {
			row = failed
		}
	}()

	if failed, ok := RunCommands(ctx, dir, label, c, tc, "setup", c.Setup, out); !ok {
		return failed, m
	}

	res := executil.RunOutput(ctx, dir, ChildEnv(tc, c, inv), out.W, inv.Name, inv.Args...)
	// Here, and not in a later pass over the report: the stack this component
	// declared is still up, and a service-dependent test rerun after teardown
	// fails because nothing is listening — which would report the most
	// reliable test in the repository as flaky, with evidence its author
	// cannot reproduce. Whether the suite passed or failed, because it is when
	// the suite failed that a disagreement is most likely and most valuable.
	gate.run(ctx, root, dir, c, inv, tc, out)
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
		failed := measure.UnmeasuredComponent(c, "the suite failed, so its coverage report describes an unfinished run")
		withTestCounts(&failed, dir, inv)
		return Failure(label, out.Rel, strings.Join(append([]string{inv.Name}, inv.Args...), " ")+" in "+c.Dir, "failed", res.Output), failed
	}
	passed := measure.Measure(ctx, root, c, inv, cfg, tc, instrument, ChildEnv(tc, c, runner.Invocation{}))
	withTestCounts(&passed, dir, inv)
	return ui.Row{Status: ui.StatusPass, Label: label, Value: "passed", Log: out.Rel}, passed
}

// withTestCounts reads the JUnit report the invocation asked for, and says why
// there is none when there is not.
//
// A reason and never silence. A component contributing no test counts is
// indistinguishable, in a history, from one that ran no tests — and the
// commonest cause is a repository whose own runner configuration sent the
// report somewhere lydite does not look, which is a thing its author can fix
// once they are told.
func withTestCounts(m *measure.Measurement, dir string, inv runner.Invocation) {
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

// ClearReport makes the component's report path ready to be written to: its
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
func ClearReport(dir, report string) error {
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

// ignoreReports keeps lydite's own output out of git, by writing a `.gitignore`
// ignoring everything into the report directory itself.
//
// lydite writes into the repository it is measuring, and what it writes must
// never become part of what it measures: a committed report directory lands in
// the diff, matches no component, and widens affected selection to everything
// on every change.
//
// The same file cmd/lydite's own ignoreReports writes, byte for byte, and a
// test there holds the two to it: a report directory whose `.gitignore` depends
// on which command created it first is one whose contents git treats
// differently from run to run.
//
// Best-effort. A report directory that cannot hold a `.gitignore` is not a
// reason to fail a run, and the failure it prevents is a slow run rather than
// a wrong answer.
func ignoreReports(dir string) {
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return
	}
	// "*" and not "**": a .gitignore ignores paths relative to itself, and a
	// single star at the top of a directory covers everything under it,
	// including this file.
	_ = os.WriteFile(path, []byte("*\n"), 0o600)
}

// Failure builds a failing row: what was run, the tail of what it printed, and
// where the whole of it is.
//
// The three together are the point. The one-line invocation says what to
// re-run, the tail says why without the reader leaving the verdict, and the
// log path is what survives when the tail is not enough — and is what a CI job
// collects and a PR comment links. rel is that path relative to the scan root,
// and empty when the output was written nowhere a reader can open.
func Failure(label, rel, what, value, output string) ui.Row {
	detail := []string{what}
	detail = append(detail, Tail(output)...)
	if rel != "" {
		detail = append(detail, "full output: "+rel)
	}
	return ui.Row{Status: ui.StatusFail, Label: label, Value: value, Detail: detail, Log: rel}
}

// TailLines is how much of a failing command's output goes under its row.
//
// Enough for a panic and its first frames, or a test runner's summary and the
// failure above it; small enough that a component failing does not reprint its
// whole suite. The rest is in the log, which the row names.
const TailLines = 40

// Tail returns the last lines of output, for the detail under a failing row.
//
// The cause is put next to the verdict deliberately. It is already in the log
// and, when streaming, already on the terminal — but a reader looking at a red
// row should not have to scroll past another component's container lifecycle
// to find out what happened, which is exactly what a real CI log does to them.
func Tail(output string) []string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil
	}
	if len(lines) > TailLines {
		lines = lines[len(lines)-TailLines:]
	}
	return lines
}

// StartServices brings the component's stack up and returns the teardown to
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
func StartServices(ctx context.Context, p Plan, label string) (func(), ui.Row, bool) {
	if p.Stack == nil {
		return func() {}, ui.Row{}, true
	}
	if err := p.Stack.Up(ctx); err != nil {
		// Down anyway: up --wait leaves the containers it did start behind
		// when one of them never became healthy, and those hold the ports the
		// next component is waiting for.
		if derr := p.Stack.Down(context.WithoutCancel(ctx)); derr != nil {
			fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", p.C.Name, derr)
		}
		return nil, Failure(label, p.Out.Rel, err.Error(), "services not started", ""), false
	}
	return func() {
		// Teardown gets a context of its own: the run's may already be
		// cancelled, and a cancelled teardown is the leak this exists to
		// prevent. Leaked containers poison the next local run, and the port
		// they hold is the next component's to bind.
		if err := p.Stack.Down(context.WithoutCancel(ctx)); err != nil {
			fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", p.C.Name, err)
		}
	}, ui.Row{}, true
}

// RunCommands runs a component's setup or teardown list, in order, stopping
// at the first failure.
//
// Through a shell, because these are free-form and repository-authored: a
// migration is `make migrate && ./seed.sh`, which argv cannot express.
func RunCommands(ctx context.Context, dir, label string, c component.Component, tc *toolchain.Env, kind string, cmds []string, out Output) (ui.Row, bool) {
	for _, cmd := range cmds {
		// #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- the command comes from the scanned repository's own component declaration, authored by whoever configured lydite for that repo, not from remote input
		// An empty invocation: a setup command gets the component's toolchain
		// and its declared environment, and not the pinned runner's directory.
		// It is the repository's own shell, not lydite's runner — a migration
		// or a seed script has no business finding `cargo nextest` on PATH
		// because lydite is about to run one.
		if res := executil.RunOutput(ctx, dir, ChildEnv(tc, c, runner.Invocation{}), out.W, "sh", "-c", cmd); !res.Ok() {
			return Failure(label, out.Rel, cmd+" failed in "+c.Dir, kind+" failed", res.Output), false
		}
	}
	return ui.Row{}, true
}

// Invocation is the plain variant of a component's suite: the fast path, and
// the only one `lydite test` wants. The coverage gate reads the instrumented
// variant, and mutation needs all three.
func Invocation(c component.Component, variant runner.Variant) (runner.Invocation, error) {
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
// Invocation itself is left alone, because mutation and the baseline
// measurement read it and neither asked for the gate: the plain variant is the
// bare `go test` mutation runs once per mutant, and a JUnit report written and
// discarded thousands of times is a process in the way of the thing being
// timed (ADR 0027).
func invocationFor(c component.Component, variant runner.Variant, gate *FlakyGate) (runner.Invocation, error) {
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
	return Invocation(c, variant)
}
