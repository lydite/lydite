package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

func newTestCmd() *cobra.Command {
	var dir string
	var components []string
	var asJSON, noColor, keepServices, stream bool
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
			rep.Add(orphanRow(cmd.Context(), dir, file))

			selected, err := file.Select(components)
			if err != nil {
				return err
			}
			if len(selected) == 0 {
				// Through the report rather than around it: --json promises
				// stdout carries a document and nothing else, and a bare
				// sentence printed here is unparseable output.
				rep.Add(ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  "test",
					Value:  "no components declared in " + component.FileName,
				})
				return renderTestReport(cmd, rep, asJSON, noColor)
			}

			for _, c := range selected {
				rep.Add(runComponent(cmd.Context(), dir, c, cfg, keepServices, stream))
			}
			return renderTestReport(cmd, rep, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&components, "component", nil, "component to run; repeatable, and every declared component by default")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	// A flag and never a key in the component declaration: whether to leave
	// services up between runs is a choice about this invocation, not a fact
	// about the repository.
	cmd.Flags().BoolVar(&keepServices, "keep-services", false, "leave each component's compose services running after the suite")
	// Every run captures; this only adds the terminal. A suite that hangs
	// prints nothing until it is killed, and its log is written but not yet
	// interesting — watching it is the case a captured file cannot serve.
	cmd.Flags().BoolVar(&stream, "stream", false, "mirror each component's output to stderr as it runs, as well as to its log")
	return cmd
}

// runComponent runs one component's suite and returns its row.
//
// A component whose services or setup commands lydite cannot supply fails
// rather than running: a suite executed without the database it declared
// reports failures naming the tests instead of the missing service, and a
// green run is worse still — it would mean the declaration was ignored and
// nobody was told.
func runComponent(ctx context.Context, root string, c component.Component, cfg config.Config, keep, stream bool) (row ui.Row) {
	label := "test(" + c.Name + ")"

	inv, err := invocation(c)
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}
	}

	log := openLog(root, c.Name, stream)
	defer log.Close()

	dir := filepath.Join(root, filepath.FromSlash(c.Dir))
	if prepared, ok := prepare(ctx, dir, label, c, cfg, log); !ok {
		return prepared
	}

	// Both teardowns are deferred before anything starts, so every path out
	// of here runs them — including a setup that failed halfway, which is
	// when a half-applied migration most needs undoing. Leaked containers
	// poison the next local run, and the port they hold is the next
	// component's to bind.
	stop, started, ok := services(ctx, dir, label, c, keep, log)
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

	file *os.File
	out  io.Writer
}

// openLog creates the component's log. A log that cannot be created is not a
// reason to skip the component, so it degrades to capture-only: the tail under
// a failing row still names the cause, which is most of what the file is for.
func openLog(root, name string, stream bool) *componentLog {
	l := &componentLog{out: io.Discard}
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
func (l *componentLog) streamed(stream bool) *componentLog {
	if stream {
		if l.file == nil {
			l.out = os.Stderr
		} else {
			l.out = io.MultiWriter(os.Stderr, l.file)
		}
	}
	return l
}

func (l *componentLog) Close() {
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

// services starts the component's compose services and returns the teardown
// to run once the suite is done.
//
// A component that declares none needs no runtime and gets no probe, so a
// repository with no services runs on a machine with no container engine at
// all.
//
// Failing to start is reported as the component failing rather than being
// pushed into the suite: a suite run against an absent database reports errors
// naming the tests, which is the one outcome worse than an unstarted service.
func services(ctx context.Context, dir, label string, c component.Component, keep bool, log *componentLog) (func(), ui.Row, bool) {
	if !c.Compose.Declared() {
		return func() {}, ui.Row{}, true
	}
	fail := func(err error) (func(), ui.Row, bool) {
		return nil, failure(label, log, err.Error(), "services not started", ""), false
	}
	stack, err := compose.Load(ctx, dir, c, log.out)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "lydite: %s services via %s\n", c.Name, stack.Runtime())
	if err := stack.Up(ctx); err != nil {
		return fail(err)
	}
	return func() {
		if keep {
			fmt.Fprintf(os.Stderr, "lydite: %s services left running\n", c.Name)
			return
		}
		// Teardown gets a context of its own: the run's may already be
		// cancelled, and a cancelled teardown is the leak this exists to
		// prevent.
		if err := stack.Down(context.WithoutCancel(ctx)); err != nil {
			fmt.Fprintf(os.Stderr, "lydite: %s: %v\n", c.Name, err)
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

// renderTestReport renders and returns the verdict as an exit code.
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
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not checked", Detail: []string{err.Error()}}
	}
	for _, e := range res.UnusedExcludes {
		fmt.Fprintf(os.Stderr, "lydite: %s: exclude %q covers no file — a directory is spelled %q\n", component.FileName, e, strings.TrimSuffix(e, "/")+"/**")
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

func renderTestReport(cmd *cobra.Command, rep *ui.Report, asJSON, noColor bool) error {
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}
