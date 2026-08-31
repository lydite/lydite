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

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "lydite",
		Short:         "Unified code-quality and security scanning for Rust, TypeScript, and Go",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(
		newScanCmd(),
		newReviewCmd(),
		newClearanceCmd(),
		newCoverageCmd(),
		newVersionCmd(),
		newUpdateCmd(),
	)
	return cmd
}
