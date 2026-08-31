package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

func newTestCmd() *cobra.Command {
	var dir string
	var components []string
	var asJSON, noColor bool
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
				rep.Add(runComponent(cmd.Context(), dir, c, cfg))
			}
			return renderTestReport(cmd, rep, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&components, "component", nil, "component to run; repeatable, and every declared component by default")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// runComponent runs one component's suite and returns its row.
//
// A component whose services or setup commands lydite cannot supply fails
// rather than running: a suite executed without the database it declared
// reports failures naming the tests instead of the missing service, and a
// green run is worse still — it would mean the declaration was ignored and
// nobody was told.
func runComponent(ctx context.Context, root string, c component.Component, cfg config.Config) ui.Row {
	label := "test(" + c.Name + ")"
	if c.Compose.Declared() {
		return ui.Row{
			Status: ui.StatusFail,
			Label:  label,
			Value:  "services not started",
			Detail: []string{
				c.Name + " declares compose services, and lydite starts none.",
				"Start them yourself and run the suite directly, or drop the compose block if the suite does not need it.",
			},
		}
	}
	if len(c.Setup) > 0 || len(c.Teardown) > 0 {
		return ui.Row{
			Status: ui.StatusFail,
			Label:  label,
			Value:  "setup not run",
			Detail: []string{
				c.Name + " declares setup or teardown commands, and lydite runs none.",
				"Run them yourself around the suite, or drop them if the suite does not need them.",
			},
		}
	}

	inv, err := invocation(c)
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}
	}

	dir := filepath.Join(root, filepath.FromSlash(c.Dir))
	if row, ok := prepare(ctx, dir, label, c, cfg); !ok {
		return row
	}
	res := executil.RunEnv(ctx, dir, env(c), inv.Name, inv.Args...)
	if res.Ok() {
		return ui.Row{Status: ui.StatusPass, Label: label, Value: "passed"}
	}
	// The runner streamed its own failures live, so the detail here names
	// what was run rather than repeating output that is already on the
	// terminal and in the CI log.
	return ui.Row{
		Status: ui.StatusFail,
		Label:  label,
		Value:  "failed",
		Detail: []string{strings.Join(append([]string{inv.Name}, inv.Args...), " ") + " in " + c.Dir},
	}
}

// prepare runs whatever the runner needs in place before the suite, and
// reports a failing row rather than letting the suite start without it.
//
// A JavaScript suite run without its node_modules fails at import, naming the
// tests rather than the absent dependencies — the same misattribution a suite
// run without its database produces, and the same reason to stop first.
func prepare(ctx context.Context, dir, label string, c component.Component, cfg config.Config) (ui.Row, bool) {
	r, ok := runner.Lookup(c.Runner)
	if !ok || r.Prepare == nil {
		return ui.Row{}, true
	}
	for _, step := range r.Prepare(dir, cfg.TypeScript.Install) {
		res := executil.RunEnv(ctx, dir, env(c), step.Name, step.Args...)
		if res.Ok() || step.Optional {
			continue
		}
		return ui.Row{
			Status: ui.StatusFail,
			Label:  label,
			Value:  "dependencies not installed",
			Detail: []string{
				strings.Join(append([]string{step.Name}, step.Args...), " ") + " failed in " + c.Dir,
				"Set typescript.install in " + config.FileName + " if this component installs differently.",
			},
		}, false
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
func renderTestReport(cmd *cobra.Command, rep *ui.Report, asJSON, noColor bool) error {
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}
