package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	testmeasure "lydite/lydite/internal/test/measure"
	"lydite/lydite/internal/ui"
)

// measurement is one component's coverage outcome for this run. The type and
// every rule about what it means live in internal/test/measure; this package
// names it for the commands that build, fold and render one.
type measurement = testmeasure.Measurement

// scorableLang is testmeasure.ScorableLang.
func scorableLang(lang runner.Lang) bool { return testmeasure.ScorableLang(lang) }

// unmeasuredComponent is testmeasure.UnmeasuredComponent.
func unmeasuredComponent(c component.Component, why string) measurement {
	return testmeasure.UnmeasuredComponent(c, why)
}

// unmeasurableComponent is testmeasure.UnmeasurableComponent.
func unmeasurableComponent(c component.Component, why string) measurement {
	return testmeasure.UnmeasurableComponent(c, why)
}

// langOf is testmeasure.LangOf.
func langOf(c component.Component) runner.Lang { return testmeasure.LangOf(c) }

// ungatedComposedRow is testmeasure.UngatedComposedRow.
func ungatedComposedRow(label string, ms []measurement, carried map[string]bool, in func(measurement) bool) ui.Row {
	return testmeasure.UngatedComposedRow(label, ms, carried, in)
}

// candidateRow saves what this run would record and says so.
//
// The row is `record` rather than `candidate`, because what a reader wants to
// know is whether the next change has a baseline to gate against, and the
// answer travels through this file whether or not the write lands here.
func candidateRow(cmd *cobra.Command, root string, doc measurementsDoc, value string) ui.Row {
	if err := writeMeasurements(root, doc); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write the candidate baseline: %v\n", err)
		return ui.Row{Status: ui.StatusContext, Label: "record",
			Value: "not recorded — the candidate baseline could not be written, so there is nothing for `lydite test record` to land"}
	}
	return ui.Row{Status: ui.StatusContext, Label: "record", Value: value}
}

// crapRow is testmeasure.CRAPRow.
func crapRow(m measurement, baseline gitstate.CRAPBaseline, gated bool) (ui.Row, []finding.Finding) {
	return testmeasure.CRAPRow(m, baseline, gated)
}

// worstOffenders is how many functions a failing CRAP row names.
const worstOffenders = testmeasure.WorstOffenders

// crapSummaryRow is testmeasure.CRAPSummaryRow.
func crapSummaryRow(scorable int, scored, carried []gitstate.CRAPEntry) (ui.Row, bool) {
	return testmeasure.CRAPSummaryRow(scorable, scored, carried)
}

// patchFindings is testmeasure.PatchFindings.
func patchFindings(row ui.Row, dir string, m measurement, scoped map[string][]int) []finding.Finding {
	return testmeasure.PatchFindings(row, dir, m, scoped)
}

// composedRows adds testmeasure.ComposedRows to rep, in the order it returns
// them.
func composedRows(rep *ui.Report, current []measurement, carried map[string]bool, baseline gitstate.Baseline, parts []patchPart, cfg config.Config) {
	for _, row := range testmeasure.ComposedRows(current, carried, baseline, parts, cfg) {
		rep.Add(row)
	}
}

// patchPart is one component's contribution to a composed patch figure.
type patchPart = testmeasure.PatchPart

// scopeToComponent is testmeasure.ScopeToComponent.
func scopeToComponent(changed map[string][]int, m measurement) map[string][]int {
	return testmeasure.ScopeToComponent(changed, m)
}

// floorSummaryRow is testmeasure.FloorSummaryRow.
func floorSummaryRow(ms []measurement, floor float64) (ui.Row, bool) {
	return testmeasure.FloorSummaryRow(ms, floor)
}

// recordable is testmeasure.Recordable.
func recordable(decl component.File) int { return testmeasure.Recordable(decl) }

// sameEntries is testmeasure.SameEntries.
func sameEntries[M ~map[string]E, E comparable](a, b M) bool { return testmeasure.SameEntries(a, b) }

// withToleratedDipsRestored is testmeasure.WithToleratedDipsRestored.
func withToleratedDipsRestored(record, baseline gitstate.Baseline, tolerance float64) gitstate.Baseline {
	return testmeasure.WithToleratedDipsRestored(record, baseline, tolerance)
}

// everything is testmeasure.Everything.
func everything(m measurement) bool { return testmeasure.Everything(m) }

// repoLabel is testmeasure.RepoLabel.
func repoLabel(metric string) string { return testmeasure.RepoLabel(metric) }

// unmeasuredRow is testmeasure.UnmeasuredRow.
func unmeasuredRow(label, why string) ui.Row { return testmeasure.UnmeasuredRow(label, why) }

// firstNonEmpty returns the first non-empty string, so a tree lookup that
// failed falls back to the commit SHA rather than writing an empty key.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// newRemovedCoverageCmd keeps the name `lydite coverage` addressable, so a
// workflow still invoking it is told what happened.
//
// Cobra's answer to an unknown command names the binary and lists what it does
// accept, which leaves a consumer to guess whether the command was renamed,
// dropped, or never existed. That guess is the same failure a silently ignored
// config key produces, one layer out: the flags this command carried are
// refused by name for exactly that reason, and the command that carried them
// should not be less clear than its flags.
//
// It accepts unknown flags so `lydite coverage --source=report` reaches this
// message rather than cobra's flag parser, which would report an unknown flag
// on a command that no longer exists at all.
func newRemovedCoverageCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "coverage",
		Hidden:             true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true,
		Short:              "removed — lydite test measures and gates coverage",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("`lydite coverage` is no longer a command: `lydite test` measures each component's coverage from its runner's instrumented variant, " +
				"and `lydite test --gate-coverage` gates it against the baseline\n" +
				"       --source, --tests, --go-report, --rust-report and --rust-lcov-report went with it; lydite writes every report itself\n" +
				"       see docs/adr/0019-coverage-per-component-gated-by-lydite-test.md")
		},
	}
}
