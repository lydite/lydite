package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/typescript"
	"lydite/lydite/internal/ui"
)

func newScanCmd() *cobra.Command {
	var dir, diffBase, baseBranch string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use: "scan",
		// A non-zero verdict is an answer, not a misuse of the command and
		// not a malfunction. Cobra prints usage and an "Error:" line for any
		// error a RunE returns, which would bury the report under the flag
		// list every time a gate failed. main owns error reporting.
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Run code-quality and security checks for every declared component",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			// Started here rather than in report(), so the duration on the
			// verdict line covers the scan and not just its rendering.
			streamDiagnostics(asJSON)
			rep := ui.NewReport("scan")

			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}

			file, err := component.Load(dir)
			if err != nil {
				return err
			}
			// An error rather than a row, because there is nothing to report
			// on: scan runs the checks each component's language implies, so
			// a repository that declares none would be scanned by nothing at
			// all while the job stayed green — a security scan that silently
			// stopped. Declaring a component is work the author can do, and
			// naming the file is what makes it one step.
			if len(file.Components) == 0 {
				return fmt.Errorf("no components declared in %s: scan runs the checks each component's language implies, so declare what this repository builds",
					filepath.Join(dir, filepath.FromSlash(component.FileName)))
			}

			// Before anything shells out to cargo/go/biome: make sure each
			// component's language toolchain is present at the version its
			// own directory declares. lydite pins every tool it runs, and
			// this is the toolchain it runs them with.
			envs, err := ensureToolchains(ctx, cmd, dir, cfg, scanUnits(file, cfg))
			if err != nil {
				return err
			}

			for _, c := range file.Components {
				lang := langOf(c)
				if lang == "" {
					// Said out loud rather than skipped. A component lydite
					// cannot derive a language for is one nothing scans, and
					// dropping it in silence reads exactly like a component
					// that was scanned and found clean.
					rep.Add(ui.Row{
						Status: ui.StatusUnmeasured,
						Label:  "scan(" + c.Name + ")",
						Value:  "not scanned — a component declaring its own command implies no language",
					})
					continue
				}
				// A language turned off in .lydite/config.yml is one whose
				// checks never run, so its components produce no rows at all
				// — a row per opted-out component trains readers to skip the
				// tag that exists to be noticed.
				if !langEnabled(lang, cfg) {
					continue
				}
				cdir := filepath.Join(dir, filepath.FromSlash(c.Dir))
				env := envs.For(c.Name).Environ()
				var results []executil.Result
				switch lang {
				case runner.Rust:
					results = rust.Check(ctx, cdir, env)
				case runner.TypeScript:
					tsResults, err := typescript.Check(ctx, cdir, env)
					if err != nil {
						return err
					}
					results = tsResults
				case runner.Go:
					results = golang.Check(ctx, cdir, env)
				}
				for _, row := range resultRows(labelled(results, c.Name)) {
					rep.Add(row)
				}
			}

			var results []executil.Result
			if cfg.Semgrep.Enabled {
				baseSHA, err := resolveDiffBase(ctx, dir, diffBase, baseBranch)
				if err != nil {
					return err
				}
				results = append(results, semgrep.Check(ctx, dir, cfg.Semgrep.Config, baseSHA))
			}

			return report(cmd, rep, results, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory to scan")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	cmd.Flags().StringVar(&diffBase, "diff-base", "", `only report findings introduced since this commit ("auto" resolves the merge-base with the base branch); empty scans everything`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	return cmd
}

// scanUnits is what each declared component needs a toolchain for, in
// declaration order.
//
// Only components whose language is enabled: `enabled: false` says lydite
// runs no check over that language's code, so provisioning its toolchain
// would download a compiler nothing is going to invoke. A component that
// declares its own command implies no language and needs nothing.
func scanUnits(file component.File, cfg config.Config) []toolchain.Unit {
	var out []toolchain.Unit
	for _, c := range file.Components {
		lang := langOf(c)
		if lang == "" || !langEnabled(lang, cfg) {
			continue
		}
		out = append(out, toolchain.Unit{Name: c.Name, Lang: lang, Dir: c.Dir})
	}
	return out
}

// langEnabled reports whether .lydite/config.yml leaves one language's checks
// switched on.
func langEnabled(l runner.Lang, cfg config.Config) bool {
	switch l {
	case runner.Rust:
		return cfg.Rust.Enabled
	case runner.TypeScript:
		return cfg.TypeScript.Enabled
	case runner.Go:
		return cfg.Go.Enabled
	}
	return false
}

// labelled names each of a component's results for the component that
// produced them — `gosec(cli)`, `cargo clippy(api)`.
//
// The name and never the directory: component.validate enforces unique names
// and not unique directories, so the name is the only one of the two unique
// by construction. It also matches how `lydite test` labels its own rows, so
// a scan row and a test row about one component carry the same token.
func labelled(results []executil.Result, component string) []executil.Result {
	out := make([]executil.Result, 0, len(results))
	for _, r := range results {
		r.Name += "(" + component + ")"
		out = append(out, r)
	}
	return out
}

// resolveDiffBase turns the --diff-base flag into a commit SHA for Semgrep's
// scan-mode --baseline-commit. "auto" resolves the same merge-base
// `lydite coverage` already gates against, so a PR's scan and coverage agree
// on what "this change" means; any other non-empty value is passed through as
// a literal ref.
//
// It's skipped entirely when SEMGREP_APP_TOKEN is set, because `semgrep ci`
// scopes itself to the diff already — resolving a merge-base there would cost
// a `git fetch` whose result nothing reads, and would newly require a full
// checkout depth from token-bearing consumers that don't need one today.
func resolveDiffBase(ctx context.Context, dir, diffBase, baseBranch string) (string, error) {
	if diffBase == "" || os.Getenv(semgrep.AppTokenEnv) != "" {
		return "", nil
	}
	if diffBase != "auto" {
		return diffBase, nil
	}
	// Deliberately an error, not a silent full-repo scan: falling back would
	// reintroduce exactly the surprise this flag exists to remove — a scan
	// that quietly changes scope, and starts blocking on findings the PR
	// never touched. A shallow checkout is a fixable CI misconfiguration
	// (fetch-depth: 0), so say so.
	baseSHA, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
	if err != nil {
		return "", fmt.Errorf("--diff-base auto: %w (a full-history checkout is required — set fetch-depth: 0)", err)
	}
	return baseSHA, nil
}

// resultRows turns each check's result into its row. It is the one place a
// Result becomes a Row, so rows a command adds as it goes and rows added at
// the end cannot render differently.
func resultRows(results []executil.Result) []ui.Row {
	rows := make([]ui.Row, 0, len(results))
	for _, r := range results {
		status := ui.StatusPass
		value := "passed"
		var detail []string
		if !r.Ok() {
			status, value = ui.StatusFail, "failed"
			detail = strings.Split(strings.TrimRight(r.Detail, "\n"), "\n")
			if len(detail) == 1 && strings.TrimSpace(detail[0]) == "" {
				detail = nil
			}
		}
		rows = append(rows, ui.Row{Status: status, Label: r.Name, Value: value, Detail: detail})
	}
	return rows
}

// report renders one row per check in the grammar docs/design/tokens.md
// specifies, and returns the run's exit code as an error so the process
// reflects the verdict.
//
// A failing check also prints its Detail, which is the only place some
// findings exist. Most tools stream their own output live through
// executil.Run, so it is already on the terminal and in the log the action
// captures; Biome does not, because lydite sends its report to a file so the
// JSON cannot be corrupted by Biome's own chatter. Printing only a status
// line left the developer to re-run the pinned toolchain by hand to find out
// what was wrong, and put nothing in the PR comment either.
func report(cmd *cobra.Command, rep *ui.Report, results []executil.Result, asJSON, noColor bool) error {
	for _, row := range resultRows(results) {
		rep.Add(row)
	}
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}
