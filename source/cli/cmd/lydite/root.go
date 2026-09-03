package main

import (
	"os"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/executil"
)

// streamDiagnostics keeps stdout free for the report.
//
// Scanners stream their findings live, which is what a developer watching a
// run wants and what the CI log captures. Under --json stdout carries a
// document, so that stream moves to stderr rather than being silenced —
// losing the findings would be a worse trade than losing their placement.
func streamDiagnostics(asJSON bool) {
	if asJSON {
		executil.StreamTo(os.Stderr)
	}
}

// baseBranchUsage documents --base-branch identically wherever it appears.
//
// Every command that measures a change against "before it" resolves the same
// merge-base, so they must agree on which branch that is — a scan and a
// coverage gate disagreeing about what this change contains is worse than
// either being wrong alone. One string, so the three cannot drift.
const baseBranchUsage = "branch on origin the change is measured against; " +
	"discovered from origin/HEAD, else whichever of main and master origin has"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "lydite",
		Short:         "Unified code-quality and security scanning for Rust, TypeScript, and Go",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(
		newScanCmd(),
		newTestCmd(),
		newReviewCmd(),
		newClearanceCmd(),
		newRemovedCoverageCmd(),
		newVersionCmd(),
		newUpdateCmd(),
	)
	return cmd
}
