package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/reviewdecision"
)

func newReviewCompareCmd() *cobra.Command {
	var dir, base, baseBranch, surfacesPath string
	cmd := &cobra.Command{
		Use:           "compare",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Compare every opted-in component's public API against the merge-base, and write the raw result",
		Long: `Compare every opted-in component's public API against the merge-base, and
write the raw result.

This is the only half of what review decides that runs any of the change's
own code: a Rust component's comparison builds and runs the head tree's own
build.rs and proc-macros. It computes no decision and reads no exemptions —
--write-surfaces names where the raw result goes, for a later
'review --surfaces' in a job that holds the publishing credential this one
need not, and never re-runs this comparison to reach it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if surfacesPath == "" {
				return fmt.Errorf("--write-surfaces names where to write the result")
			}
			ctx := cmd.Context()
			baseSHA, err := reviewdecision.ResolveBase(ctx, dir, base, baseBranch)
			if err != nil {
				return err
			}
			// Never guarded: review compare never publishes, so there is no
			// credential in this process for a Rust comparison to reach.
			results, err := reviewdecision.CompareSurfaces(ctx, dir, baseSHA, false, commandToolchains{cmd}, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return reviewdecision.WriteSurfaces(surfacesPath, baseSHA, results)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose .lydite/components.yml applies")
	cmd.Flags().StringVar(&base, "base", "auto", `commit this change is measured against ("auto" resolves the merge-base with the base branch)`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	cmd.Flags().StringVar(&surfacesPath, "write-surfaces", "", "write the raw comparison result to this path")
	return cmd
}
