package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/flow"
	reviewflow "lydite/lydite/internal/flows/review"
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
			compare, err := reviewflow.NewCompare()
			if err != nil {
				return err
			}
			_, err = compare.Run(cmd.Context(), reviewflow.CompareParams{
				Dir:        dir,
				Base:       base,
				BaseBranch: baseBranch,
				Toolchains: commandToolchains{cmd},
				Progress:   cmd.ErrOrStderr(),
				Path:       surfacesPath,
			}.Inputs())
			return compareError(err)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose .lydite/components.yml applies")
	cmd.Flags().StringVar(&base, "base", "auto", `commit this change is measured against ("auto" resolves the merge-base with the base branch)`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	cmd.Flags().StringVar(&surfacesPath, "write-surfaces", "", "write the raw comparison result to this path")
	return cmd
}

// compareError is a comparison's failure as this command reports it: the
// stage's own error, not the flow's framing of it.
func compareError(err error) error {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}
