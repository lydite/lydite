package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/config"
	"lydite/lydite/internal/detect"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/typescript"
	"lydite/lydite/internal/ui"
)

func newScanCmd() *cobra.Command {
	var dir, diffBase string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use: "scan",
		// A non-zero verdict is an answer, not a misuse of the command and
		// not a malfunction. Cobra prints usage and an "Error:" line for any
		// error a RunE returns, which would bury the report under the flag
		// list every time a gate failed. main owns error reporting.
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Run code-quality and security checks for every detected ecosystem",
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

			ecosystems, err := detect.Ecosystems(dir, cfg.AllExcludes())
			if err != nil {
				return err
			}
			if len(ecosystems) == 0 {
				// Through the report, not around it: --json promises stdout
				// carries a document and nothing else, and a bare sentence
				// printed here is unparseable output on a path no scan of
				// this repository ever reaches.
				rep.Add(ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  "scan",
					Value:  "no supported ecosystem detected under " + dir,
				})
				return report(cmd, rep, nil, asJSON, noColor)
			}

			// Before anything shells out to cargo/go/npx: make sure each
			// enabled ecosystem's language toolchain is present at the
			// version the repo declares. lydite pins every tool it runs but
			// used to assume the toolchain it runs them with.
			if err := ensureToolchains(ctx, cmd, dir, cfg, enabledEcosystems(ecosystems, cfg)); err != nil {
				return err
			}

			var results []executil.Result
			for _, e := range ecosystems {
				switch e {
				case detect.Rust:
					if !cfg.Rust.Enabled {
						continue
					}
					rustResults, err := rust.Check(ctx, dir, cfg.Rust.Exclude)
					if err != nil {
						return err
					}
					results = append(results, rustResults...)
				case detect.TypeScript:
					if !cfg.TypeScript.Enabled {
						continue
					}
					tsResults, err := typescript.Check(ctx, dir, cfg.TypeScript.Exclude)
					if err != nil {
						return err
					}
					results = append(results, tsResults...)
				case detect.Go:
					if !cfg.Go.Enabled {
						continue
					}
					goResults, err := golang.Check(ctx, dir, cfg.Go.Exclude)
					if err != nil {
						return err
					}
					results = append(results, goResults...)
				}
			}
			if cfg.Semgrep.Enabled {
				baseSHA, err := resolveDiffBase(ctx, dir, diffBase)
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
	cmd.Flags().StringVar(&diffBase, "diff-base", "", `only report findings introduced since this commit ("auto" resolves the merge-base with origin/main); empty scans everything`)
	return cmd
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
func resolveDiffBase(ctx context.Context, dir, diffBase string) (string, error) {
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
	baseSHA, err := gitstate.BaseSHA(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("--diff-base auto: %w (a full-history checkout is required — set fetch-depth: 0)", err)
	}
	return baseSHA, nil
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
		rep.Add(ui.Row{Status: status, Label: r.Name, Value: value, Detail: detail})
	}
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}
