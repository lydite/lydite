package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// surfaceDocument is what review compare writes and review --surfaces reads:
// the merge-base this run resolved, and each opted-in component's raw
// comparison result.
//
// The base travels with the results rather than being re-resolved by the
// reader: origin's default branch can move between two independent
// resolutions of "auto", and a decision computed against a different base
// than the one the comparisons actually ran against would be answering a
// question nobody asked.
type surfaceDocument struct {
	Base    string              `json:"base"`
	Results []surfaceComparison `json:"results"`
}

// writeSurfaces records computeAPISurfaces's result, for review --surfaces to
// read in a later, separate invocation.
func writeSurfaces(path, base string, results []surfaceComparison) error {
	f, err := os.Create(path) // #nosec G304 -- a workflow's own artifact path, not attacker-controlled
	if err != nil {
		return fmt.Errorf("writing the surface comparisons: %w", err)
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(surfaceDocument{Base: base, Results: results})
}

// readSurfaces is writeSurfaces's inverse.
func readSurfaces(path string) (surfaceDocument, error) {
	f, err := os.Open(path) // #nosec G304 -- a workflow's own artifact path, not attacker-controlled
	if err != nil {
		return surfaceDocument{}, fmt.Errorf("reading the surface comparisons: %w", err)
	}
	defer func() { _ = f.Close() }()
	var doc surfaceDocument
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return surfaceDocument{}, fmt.Errorf("reading the surface comparisons: %w", err)
	}
	if doc.Base == "" {
		return surfaceDocument{}, fmt.Errorf("the surface document names no base commit")
	}
	return doc, nil
}

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
			baseSHA, err := resolveReviewBase(ctx, dir, base, baseBranch)
			if err != nil {
				return err
			}
			results, err := computeAPISurfaces(ctx, cmd, dir, baseSHA)
			if err != nil {
				return err
			}
			return writeSurfaces(surfacesPath, baseSHA, results)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose .lydite/components.yml applies")
	cmd.Flags().StringVar(&base, "base", "auto", `commit this change is measured against ("auto" resolves the merge-base with the base branch)`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	cmd.Flags().StringVar(&surfacesPath, "write-surfaces", "", "write the raw comparison result to this path")
	return cmd
}
