package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/flow"
	scanflow "lydite/lydite/internal/flows/scan"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/scanlang"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/shell"
	scanstages "lydite/lydite/internal/stages/scan"
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
			// Both before the flow runs: the report's clock starts here, so the
			// duration on the verdict line covers the scan and not just its
			// rendering, and every check the flow runs streams where --json
			// needs it to.
			streamDiagnostics(asJSON)
			rep := ui.NewReport("scan")

			scan, err := scanflow.New()
			if err != nil {
				return err
			}
			r, err := scan.Run(cmd.Context(), scanflow.Params{
				Dir:         dir,
				DiffBase:    diffBase,
				BaseBranch:  baseBranch,
				Toolchains:  scanToolchains{cmd},
				Environment: scanEnvironment{},
				Diagnostics: cmd.ErrOrStderr(),
				// `semgrep ci` scopes itself to the diff when a token is set,
				// which decides the base Semgrep is handed. Read here because
				// no stage reads the process environment.
				SemgrepAppToken: os.Getenv(semgrep.AppTokenEnv) != "",
			}.Inputs())
			if err != nil {
				return scanError(err)
			}

			if err := recordComponents(rep, dir, r); err != nil {
				return err
			}
			root, err := rootScanned(r)
			if err != nil {
				return err
			}

			// A run in which no check ran says so, in a row of its own.
			//
			// Counting rows is not the test for that: a component nothing
			// scans adds a row per gate that does not apply to it and a
			// deduplicated one adds one of its own, so a repository whose
			// every component declares a raw command, with
			// Semgrep off, produces a document of amber rows and has executed
			// nothing. Each opt-out is the repository's to make and none is
			// reported on its own; all of them together is a different fact.
			//
			// The row is `unmeasured`, so the verdict stays `pass` and the
			// exit code 0. That is the grammar's rule and not an oversight
			// here: refer, unmeasured and dropped all render amber and only
			// refer votes, which is what lets a check that did not run be
			// visibly distinct from one that passed without turning every
			// opt-out into a failing build. Every state reached here is one
			// the repository asked for in its own configuration — a language
			// switched off, a component declaring its own command — so failing
			// it would be lydite refusing a configuration it was handed. What
			// must not happen is silence, and the row and its `--json` status
			// are what remove it.
			if !ranAnyCheck(rep) && len(root.results) == 0 {
				rep.Add(ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  "scan",
					Value:  "nothing ran — no declared component has a language lydite checks, or every one of them is disabled",
				})
			}

			// Root-scoped, so each crash names no component — the bucket
			// their findings already sit in.
			rep.AddCrashed(root.crashes...)
			return report(cmd, rep, dir, root.results, root.findings, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory to scan")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	cmd.Flags().StringVar(&diffBase, "diff-base", "", `only report findings introduced since this commit ("auto" resolves the merge-base with the base branch); empty scans everything`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	return cmd
}

// scanToolchains is the toolchain provisioning the scan flow is handed: the
// command's own, reporting what it downloads on the command's stderr.
type scanToolchains struct{ cmd *cobra.Command }

func (t scanToolchains) Ensure(ctx context.Context, dir string, cfg config.Config, units []toolchain.Unit) (toolchain.Envs, error) {
	return ensureToolchains(ctx, t.cmd, dir, cfg, units)
}

// scanEnvironment composes a component's declared environment exactly as
// `lydite test` composes it for the component's suite: a Rust component
// declaring SQLX_OFFLINE or a Go one declaring CGO_ENABLED needs it to build at
// all. No invocation directories, since scan runs lydite's own pinned tools by
// absolute path.
type scanEnvironment struct{}

func (scanEnvironment) Compose(tc *toolchain.Env, c component.Component) []string {
	return childEnv(tc, c, runner.Invocation{})
}

func (scanEnvironment) Declared(c component.Component) (dirs, vars []string) {
	return splitPath(env(c))
}

// scanError is a run's failure as this command reports it: the stage's own
// error, not the flow's framing of it.
func scanError(err error) error {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

// recordComponents puts every declared component into the report, in
// declaration order: each one's rows where it is declared, so a reader finds
// a component's checks, its licence verdict and any reason nothing ran over it
// together, and the document reads in the order the declaration does.
//
// A scanned component's check results and licence verdict sit at the same
// index as its plan entry, which is what keeps one component's verdict from
// ever being rendered under another's name.
func recordComponents(rep *ui.Report, root string, r *flow.Result) error {
	planned, err := flow.Output[scanstages.PlanComponentsOut](r, scanflow.StagePlanComponents)
	if err != nil {
		return err
	}
	checked, err := flow.Output[scanstages.RunChecksOut](r, scanflow.StageRunChecks)
	if err != nil {
		return err
	}
	gated, err := flow.Output[scanstages.GateLicencesOut](r, scanflow.StageGateLicences)
	if err != nil {
		return err
	}
	for i, p := range planned.Plan {
		name := p.Component.Name
		switch p.Disposition {
		case scanstages.Unscanned:
			// Said out loud rather than skipped. A component lydite has no
			// scanner for is one nothing scans, and dropping it in silence
			// reads exactly like a component that was scanned and found clean.
			for _, row := range unscannedRows(name, p.Lang) {
				rep.Add(row)
			}
		case scanstages.Disabled:
			// A language turned off in .lydite/config.yml is one whose checks
			// never run, so its components produce no rows at all — a row per
			// opted-out component trains readers to skip the tag that exists to
			// be noticed. A language that is off until a repository switches it
			// on is the exception: nobody opted out, so its components say
			// which key would run them.
			for _, row := range offByDefaultRows(name, p.Lang) {
				rep.Add(row)
			}
		case scanstages.Duplicate:
			// Said, not dropped. A consumer keying rows by component name
			// would otherwise lose this one with nothing to separate "already
			// covered" from "never declared" — the same reason a component
			// nothing scans gets rows.
			rep.Add(ui.Row{
				Status: ui.StatusUnmeasured,
				Label:  "scan(" + name + ")",
				Value:  "not scanned separately — same directory and environment as " + p.DuplicateOf,
			})
		case scanstages.Scan:
			ch := checked.Checks[i]
			rep.AddCrashed(ch.Crashes...)
			record(rep, root, ch.Results, ch.Findings)
			recordLicence(rep, name, p.Lang, gated.Licences[i])
		default:
			// Refused rather than rendered as nothing: a component the report
			// is silent about reads exactly like one that was scanned and
			// found clean.
			return fmt.Errorf("component %s: %q is a disposition the scan report has no rows for", name, p.Disposition)
		}
	}
	return nil
}

// recordLicence puts one scanned component's licence verdict into the report:
// the bucket named crashed where the gate made no claim it could stand behind,
// the row, and the located claims a failing verdict is about.
//
// A Rust row names the document that decided the licences as well as the
// verdict. A Rust component can be governed by lydite's policy, by its own
// deny.toml or by neither, and those three are answered by edits to different
// files — or by no edit at all.
func recordLicence(rep *ui.Report, name string, lang runner.Lang, v scanstages.LicenceVerdict) {
	rep.AddCrashed(v.Crashes...)
	label := licence.Gate + "(" + name + ")"
	if !v.Gated {
		// A row rather than nothing: a licence row absent from a scanned
		// component reads as a gate that ran and found nothing.
		rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: label,
			Value: "not measured — " + string(lang) + " declares no dependency set to read licences from"})
		return
	}
	// Asked before the comparison is read: where the dependencies could not
	// be read the comparison is the zero value, which carries no verdict.
	if v.Err != nil {
		// Unmeasured and never fail: a component whose own dependencies could
		// not be enumerated has had nothing decided about it, and a red row
		// here would ask its author to answer for a claim the gate never made.
		rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: label,
			Value: "the component's dependencies could not be read", Detail: []string{v.Err.Error()}})
		return
	}
	row := licenceRow(label, v.Comparison)
	if lang == runner.Rust {
		if v.PolicySource == rust.PolicyFromConsumer && v.Comparison.Verdict == licence.VerdictFail {
			// A component's own deny.toml is evaluated whole and absolutely,
			// with no base in it, so the count is every crate it rejected
			// rather than what this change introduced.
			row.Value = fmt.Sprintf("%d non-conforming licence(s)", len(v.Comparison.Pairs))
		}
		row.Value += " — " + policySourceSays(v.PolicySource)
	}
	rep.Add(row)
	rep.AddFindings(v.Findings...)
}

// rootScans is what the root-scoped gates answered — Semgrep's and then
// gitleaks's, each only where it ran.
type rootScans struct {
	results  []executil.Result
	findings []finding.Finding
	crashes  []finding.Crash
}

// rootScanned is what the root-scoped gates answered. A gate its configuration
// switched off contributes nothing: its stage was skipped, and a zero result
// in its place would render as a check that ran.
func rootScanned(r *flow.Result) (rootScans, error) {
	var out rootScans
	if !r.Skipped(scanflow.StageSemgrep) {
		sg, err := flow.Output[scanstages.SemgrepOut](r, scanflow.StageSemgrep)
		if err != nil {
			return rootScans{}, err
		}
		out.results = append(out.results, sg.Result)
		out.findings = append(out.findings, sg.Findings...)
		out.crashes = append(out.crashes, sg.Crashes...)
	}
	if !r.Skipped(scanflow.StageSecrets) {
		sec, err := flow.Output[scanstages.SecretsOut](r, scanflow.StageSecrets)
		if err != nil {
			return rootScans{}, err
		}
		out.results = append(out.results, sec.Result)
		out.findings = append(out.findings, sec.Findings...)
		out.crashes = append(out.crashes, sec.Crashes...)
	}
	return out, nil
}

// scannedLang reports whether lydite has checks for a language at all. It reads
// scanlang's list, the one internal/orphan also reads, so the scan and the
// unscanned warning cannot disagree about which languages a scanner exists for.
//
// Not derived from runner.Runs: a language that has a runner and no scanner
// would reach langEnabled, which answers false for every language it has no key
// for, and be skipped as silently as an opt-out the repository never stated.
func scannedLang(l runner.Lang) bool { return scanlang.Scanned(l) }

// unscannedRows is what a component lydite has no scanner for contributes to the
// report: one row per gate a scanned component gets, each saying why that gate
// in particular did not run.
//
// A row per gate rather than one for the component, because a gate that is
// absent from the document is indistinguishable from one that ran and found
// nothing — a consumer reading every component's licence verdict, or a reader
// looking down the licence rows, finds this one answered rather than missing.
// Each row's reason is the gate's own: a licence gate read no dependency set,
// and that is a different sentence from no linter having run.
//
// Every row is `unmeasured`, so the verdict stays `pass`: a component declaring
// its own command is a configuration the repository is entitled to state, and
// what must not happen is silence about it.
func unscannedRows(name string, lang runner.Lang) []ui.Row {
	why := unscannedReason(lang)
	return []ui.Row{
		{Status: ui.StatusUnmeasured, Label: "scan(" + name + ")",
			Value: "not scanned — " + why + ", so no linter, vulnerability or SAST check runs over it"},
		{Status: ui.StatusUnmeasured, Label: licence.Gate + "(" + name + ")",
			Value: "not measured — " + why + ", so there is no dependency set to read licences from"},
		{Status: ui.StatusUnmeasured, Label: "findings(" + name + ")",
			Value: "not counted — " + why + ", so no gate reports a finding count for it"},
	}
}

// unscannedReason is why no scanner applies to a component. The two cases are
// not one sentence: a raw command states no language at all, while a language
// lydite recognises as source and runs no tool over states one nothing checks,
// and the declaration an author would change differs between them.
func unscannedReason(lang runner.Lang) string {
	if lang == "" {
		return "the component declares a raw command, which implies no language"
	}
	return "the component's language, " + string(lang) + ", has no scanner in lydite"
}

// offByDefaultRows is what a component contributes when its language is one
// lydite leaves switched off until a repository switches it on: one row per
// gate a scanned component gets, each naming the key that would run it.
//
// Nothing for any other language. Go, Rust and TypeScript are on unless a
// repository says otherwise, so one of them being off is an opt-out the
// repository stated, and its components produce no rows. Shell is off unless a
// repository says otherwise, so a `lang: shell` component in a repository that
// never mentioned shell is not an opt-out at all — and silence about it reads
// exactly like a script that was checked and found clean.
//
// Every row is `unmeasured`, so the verdict stays `pass`: the component is a
// declaration the repository is entitled to carry for the orphan gate alone.
func offByDefaultRows(name string, lang runner.Lang) []ui.Row {
	if lang != runner.Shell {
		return nil
	}
	why := "shell's checks are off unless shell.enabled: true is set in " + config.FileName
	return []ui.Row{
		{Status: ui.StatusUnmeasured, Label: "scan(" + name + ")",
			Value: "not scanned — " + why + ", so ShellCheck does not run over it"},
		{Status: ui.StatusUnmeasured, Label: licence.Gate + "(" + name + ")",
			Value: "not measured — " + why + ", and shell declares no dependency set to read licences from either way"},
		{Status: ui.StatusUnmeasured, Label: "findings(" + name + ")",
			Value: "not counted — " + why + ", so no gate reports a finding count for it"},
	}
}

// langEnabled reports whether .lydite/config.yml leaves one language's checks
// switched on.
func langEnabled(l runner.Lang, cfg config.Config) bool {
	return scanlang.Enabled(l, cfg)
}

// scannerGates is the gates a language's checks report their findings under,
// which is what makes a count of nought distinguishable from a gate that never
// applied to a component at all.
//
// Each language package names its own, so the set cannot drift from the checks
// that package runs. Derived from the language rather than from the rows a scan
// wrote, for the reason internal/finding exists at all: a row's label is prose
// — `gosec(cli)` — and reading a gate and a component back out of it is the
// text-scraping the findings channel was built to remove.
func scannerGates(lang runner.Lang) []string {
	switch lang {
	case runner.Rust:
		return rust.FindingGates()
	case runner.TypeScript:
		return typescript.FindingGates()
	case runner.Go:
		return golang.FindingGates()
	case runner.Shell:
		return shell.FindingGates()
	}
	return nil
}

// policySourceSays names the document that decided a Rust component's licences,
// on its row.
//
// Which one it was cannot be read off the verdict, and a reader told only that
// nothing was gated has no way to find the file an edit would go in.
func policySourceSays(s rust.PolicySource) string {
	switch s {
	case rust.PolicyFromLydite:
		return "policy from " + config.FileName
	case rust.PolicyFromConsumer:
		return "policy from the component's own " + rust.DenyConfigFile
	case rust.PolicyFromNone:
		return "no " + rust.DenyConfigFile + " either, so no licence check ran"
	}
	return string(s)
}

// licenceRow renders one component's comparison.
//
// Only a gating verdict renders green or red. A policy nobody stated, a run
// given no diff base and a base that could not be built each gate nothing, and
// rendering any of them as `pass` is a gate that never ran reported as one that
// ran and found nothing.
func licenceRow(label string, c licence.Comparison) ui.Row {
	row := ui.Row{Label: label, Detail: licenceDetail(c.Pairs)}
	switch c.Verdict {
	case licence.VerdictPass:
		row.Status, row.Value = ui.StatusPass, "passed"
	case licence.VerdictFail:
		row.Status, row.Value = ui.StatusFail,
			fmt.Sprintf("%d non-conforming licence(s) introduced against the merge-base", len(c.Pairs))
	case licence.VerdictUnmeasured:
		row.Status, row.Value = ui.StatusUnmeasured, c.Reason
	case licence.VerdictContext:
		row.Status, row.Value = ui.StatusContext,
			fmt.Sprintf("%d non-conforming dependencies, gating nothing — no diff base to compare against", len(c.Pairs))
	case licence.VerdictNotConfigured:
		row.Status, row.Value = ui.StatusContext,
			"not configured — state licence.policy.allow in "+config.FileName
	}
	return row
}

// licenceDetail names each pair the verdict is about, so a row a reader cannot
// act on names the dependency and the licence rather than only a count.
func licenceDetail(pairs []licence.Dependency) []string {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]string, 0, len(pairs))
	for _, d := range pairs {
		entry := d.Package
		if d.Version != "" {
			entry += " " + d.Version
		}
		out = append(out, entry+": "+d.Licence)
	}
	return out
}

// ranAnyCheck reports whether any row records a check that executed. An
// unmeasured row is a component saying why it has no check, which is the
// opposite.
func ranAnyCheck(rep *ui.Report) bool {
	for _, row := range rep.Rows() {
		if row.Status != ui.StatusUnmeasured {
			return true
		}
	}
	return false
}

// resultRows turns each check's result into its row. It is the one place a
// Result becomes a Row, so rows a command adds as it goes and rows added at
// the end cannot render differently.
//
// Every row names the log holding the whole of what its check printed, which
// is what lets a pull-request comment reach past the tail in Detail. A passing
// check's output is kept too: it is what a reader consults to find out what a
// clean run actually looked at.
func resultRows(root string, results []executil.Result) []ui.Row {
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
		rows = append(rows, ui.Row{
			Status: status, Label: r.Name, Value: value, Detail: detail,
			Log: checkLog(root, r.Name, r.Output),
		})
	}
	return rows
}

// record puts one batch of check results into the report: a row each, and the
// located claims they made.
//
// One call and not a pair, because the rows and the findings are the same
// results read twice — a caller that adds one and forgets the other publishes
// a comment saying a check failed and a review with nothing on the line it
// failed at. The claims arrive already labelled and anchored by the stage that
// ran the checks, so they are added exactly as given.
func record(rep *ui.Report, root string, results []executil.Result, found []finding.Finding) {
	for _, row := range resultRows(root, results) {
		rep.Add(row)
	}
	rep.AddFindings(found...)
}

// report renders one row per check in the grammar docs/design/tokens.md
// specifies, and returns the run's exit code as an error so the process
// reflects the verdict.
//
// A failing check also prints its Detail, which is the only place some
// findings' full text exists. Most tools stream their own output live
// through executil.Run, so it is already on the terminal and in the log the
// action captures; Biome's report never reaches the terminal at all, because
// lydite sends it to a file so the JSON cannot be corrupted by Biome's own
// chatter. clippy, cargo-audit and cargo-deny run once in JSON mode, so their
// stream is that same JSON rather than a second, richer rendering worth
// reprinting, and Detail carries the claim instead. Printing only a status
// line left the developer to re-run the pinned toolchain by hand to find out
// what was wrong, and put nothing in the PR comment either.
func report(cmd *cobra.Command, rep *ui.Report, root string, results []executil.Result, found []finding.Finding, asJSON, noColor bool) error {
	record(rep, root, results, found)
	saveDocument(root, rep)
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}
