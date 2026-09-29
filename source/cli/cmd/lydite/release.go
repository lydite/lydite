package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/flow"
	releaseflow "lydite/lydite/internal/flows/release"
	releasestages "lydite/lydite/internal/stages/release"
	"lydite/lydite/internal/ui"
)

func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "release",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Checks on the tag a release is about to publish",
	}
	cmd.AddCommand(newReleaseCheckCmd())
	return cmd
}

// newReleaseCheckCmd refuses a tag whose range declares a break its bump does
// not admit.
//
// At pull-request time a declared break is referred, because the version is
// not chosen yet and there is nothing to check the claim against. At tag time
// there is: the number itself, and it is the entire warning a consumer
// resolving `latest` ever gets. See
// docs/adr/0045-a-tag-that-is-not-the-breaking-bump-cannot-carry-a-declared-break.md.
func newReleaseCheckCmd() *cobra.Command {
	var dir, tag string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use:           "check",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Refuse a tag whose range declares a break its bump does not admit",
		Long: `Refuse a tag whose commit range declares a breaking change its bump does not admit.

A breaking change lands on a tag whose leftmost non-zero version component
increases relative to the previous release: the minor while the line is 0.x, so
v0.2.0 → v0.3.0 carries one and v0.2.0 → v0.2.1 does not, and the major from
v1.0.0 onward.

The previous release is the semver-highest tag below this one, never the commit
graph's parent, and a prerelease is checked like any other tag against the last
stable one. The first tag has no predecessor: the range is empty, and the check
says so rather than reporting a clean range.

This reads declarations, not API surfaces. It looks for the breaking-change
markers an author wrote — the conventional-commit ` + "`!`" + ` and a BREAKING CHANGE
footer — in the messages of the commits the range contains, and it never
re-derives whether an API actually broke. A pass is therefore not evidence that
nothing broke, only that nothing that was declared is under-versioned; an
undeclared break is caught by ` + "`lydite review`" + ` at pull-request time.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReleaseCheck(cmd.Context(), cmd, dir, tag, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory of the repository whose tags are read")
	cmd.Flags().StringVar(&tag, "tag", "", "tag being released (defaults to the tag the run is on, else the checked-out tag)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON on stdout")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// declarationsLabel is the one label every outcome of the check reports under,
// so a consumer reading the document finds the verdict in the same place
// whatever the range turned out to hold.
const declarationsLabel = "declared breaks"

func runReleaseCheck(ctx context.Context, cmd *cobra.Command, dir, tag string, asJSON, noColor bool) error {
	streamDiagnostics(asJSON) // [lydite:exclude_from_mutation][every command calls this by convention, but release check runs no executil.Run that could stream — nothing here observes whether it ran]

	check, err := releaseflow.New()
	if err != nil {
		return err
	}
	// The report's duration is measured from here, so it covers the run
	// whose verdict it carries.
	rep := ui.NewReport("release")
	// GITHUB_REF_NAME is the short name of whatever ref triggered a run, and is
	// a tag only when GITHUB_REF_TYPE says so — on a branch build it holds a
	// branch name, and checking a branch name as a version would report a
	// misconfiguration as a malformed tag. Both travel to the flow, which reads
	// the name only when the type is a tag.
	r, err := check.Run(ctx, releaseflow.Params{
		Dir:     dir,
		Tag:     tag,
		RefType: os.Getenv("GITHUB_REF_TYPE"),
		RefName: os.Getenv("GITHUB_REF_NAME"),
	}.Inputs())
	if err != nil {
		return releaseError(err)
	}
	row, err := releaseRow(r)
	if err != nil {
		return err
	}
	return writeReleaseReport(cmd, rep, asJSON, noColor, row)
}

// releaseError is a run's failure as this command reports it: the stage's own
// error, not the flow's framing of it, and a run with no tag to check named by
// the three places one can come from.
func releaseError(err error) error {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		err = failed.Err
	}
	if errors.Is(err, releasestages.ErrNoTag) {
		return errors.New("release check needs the tag it is checking: pass --tag, " +
			"run where GITHUB_REF_NAME names a tag, or check the tag out")
	}
	return err
}

// releaseRow is the verdict on the range a run read.
//
// A first release has no range, so judge never ran and its output is never
// read: the row says the range is empty rather than reporting a clean one.
func releaseRow(r *flow.Result) (ui.Row, error) {
	resolved, err := flow.Output[releasestages.ResolveTagOut](r, releaseflow.StageResolveTag)
	if err != nil {
		return ui.Row{}, err
	}
	previous, err := flow.Output[releasestages.PreviousTagOut](r, releaseflow.StagePreviousTag)
	if err != nil {
		return ui.Row{}, err
	}
	if previous.First {
		return ui.Row{
			Status: ui.StatusPass,
			Label:  declarationsLabel,
			Value:  fmt.Sprintf("none — %s is the first release and its range is empty", resolved.Tag),
		}, nil
	}
	judged, err := flow.Output[releasestages.JudgeOut](r, releaseflow.StageJudge)
	if err != nil {
		return ui.Row{}, err
	}
	if len(judged.Declaring) == 0 {
		return ui.Row{
			Status: ui.StatusPass,
			Label:  declarationsLabel,
			Value:  fmt.Sprintf("none in the %d commits in %s", judged.Count, judged.Range),
		}, nil
	}
	if judged.Admits {
		return ui.Row{
			Status: ui.StatusPass,
			Label:  declarationsLabel,
			Value:  fmt.Sprintf("%d of the %d commits in %s, and this bump admits one", len(judged.Declaring), judged.Count, judged.Range),
			Detail: judged.Declaring,
		}, nil
	}
	return ui.Row{
		Status: ui.StatusFail,
		Label:  declarationsLabel,
		Value:  fmt.Sprintf("%d of the %d commits in %s, and this bump admits none", len(judged.Declaring), judged.Count, judged.Range),
		Detail: append(judged.Declaring,
			fmt.Sprintf("a declared break lands where the leftmost non-zero component of %s increases", previous.Previous)),
	}, nil
}

// writeReleaseReport renders the verdict and returns the exit code it owns.
func writeReleaseReport(cmd *cobra.Command, rep *ui.Report, asJSON, noColor bool, row ui.Row) error {
	rep.Add(row)
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}
