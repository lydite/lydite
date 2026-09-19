package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"lydite/lydite/internal/declaration"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
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
	streamDiagnostics(asJSON)

	tag = releaseTag(ctx, dir, tag)
	if tag == "" {
		return fmt.Errorf("release check needs the tag it is checking: pass --tag, " +
			"run where GITHUB_REF_NAME names a tag, or check the tag out")
	}
	rep := ui.NewReport("release")

	// An invalid tag arrives back as an error rather than a verdict: a range
	// this cannot place is a check that did not run, and the alternative here
	// is publishing a tag nobody looked at.
	previous, ok, err := gitstate.PreviousTag(ctx, dir, tag)
	if err != nil {
		return err
	}
	if !ok {
		return writeReleaseReport(cmd, rep, asJSON, noColor, ui.Row{
			Status: ui.StatusPass,
			Label:  declarationsLabel,
			Value:  fmt.Sprintf("none — %s is the first release and its range is empty", tag),
		})
	}

	// The range ends at HEAD, never at the tag string. A tag-triggered run is
	// checked out detached at the pushed tag, so HEAD is that tag's commit —
	// and reading to HEAD is also what lets a release be checked from the
	// commit it is about to be cut at, before the tag object exists.
	messages, err := gitstate.CommitMessages(ctx, dir, previous, "HEAD")
	if err != nil {
		return fmt.Errorf("the commits in %s..HEAD cannot be read: %w"+
			"\n       %s must exist here as a tag and its commit must be present and reachable from HEAD:"+
			"\n       check out with `fetch-depth: 0` and the repository's tags fetched", previous, err, previous)
	}

	var declaring []string
	for _, message := range messages {
		if declaration.Declared(message) {
			declaring = append(declaring, subject(message))
		}
	}
	// The range the report names is the version range the verdict is about,
	// which is what a consumer upgrades along. HEAD is the revision the commits
	// were read to and is a ref that moves, so naming it would describe the
	// release by something that no longer points there when anybody looks.
	rng := previous + ".." + tag
	if len(declaring) == 0 {
		return writeReleaseReport(cmd, rep, asJSON, noColor, ui.Row{
			Status: ui.StatusPass,
			Label:  declarationsLabel,
			Value:  fmt.Sprintf("none in the %d commits in %s", len(messages), rng),
		})
	}
	if bumpAdmitsBreak(previous, tag) {
		return writeReleaseReport(cmd, rep, asJSON, noColor, ui.Row{
			Status: ui.StatusPass,
			Label:  declarationsLabel,
			Value:  fmt.Sprintf("%d of the %d commits in %s, and this bump admits one", len(declaring), len(messages), rng),
			Detail: declaring,
		})
	}
	return writeReleaseReport(cmd, rep, asJSON, noColor, ui.Row{
		Status: ui.StatusFail,
		Label:  declarationsLabel,
		Value:  fmt.Sprintf("%d of the %d commits in %s, and this bump admits none", len(declaring), len(messages), rng),
		Detail: append(declaring,
			fmt.Sprintf("a declared break lands where the leftmost non-zero component of %s increases", previous)),
	})
}

// releaseTag is the tag being released, in descending order of how explicitly
// it was stated: what a caller passed, then the tag the workflow run is on,
// then the tag checked out here.
//
// GITHUB_REF_NAME is the short name of whatever ref triggered a run, so it is
// read only when GITHUB_REF_TYPE says that ref is a tag — on a branch build it
// holds a branch name, and checking a branch name as a version would report a
// misconfiguration as a malformed tag.
func releaseTag(ctx context.Context, dir, tag string) string {
	if tag != "" {
		return tag
	}
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		if name := os.Getenv("GITHUB_REF_NAME"); name != "" {
			return name
		}
	}
	// --exact-match, so a commit that merely descends from a tag resolves to
	// nothing: the check is about the tag this commit is, not the one before
	// it.
	if r := executil.RunQuiet(ctx, dir, "git", "describe", "--tags", "--exact-match", "HEAD"); r.Ok() {
		return strings.TrimSpace(r.Output)
	}
	return ""
}

// subject is a commit's first line, which is what identifies it to a person
// reading the failure. The whole message is what declaration reads, because a
// break may be declared in a footer.
func subject(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(line)
}

// bumpAdmitsBreak reports whether moving from previous to tag increases the
// leftmost non-zero component of previous, which is the bump ADR 0010 requires
// a declared break to land on.
//
// The comparison is over the prefix through that component rather than over
// the component alone, so a release crossing eras answers correctly:
// v0.2.0 → v1.0.0 admits a break even though the minor it is measured on went
// from 2 to 0.
func bumpAdmitsBreak(previous, tag string) bool {
	p, t := releaseVersion(previous), releaseVersion(tag)
	switch {
	case semver.Major(p) != "v0":
		return semver.Compare(semver.Major(t), semver.Major(p)) > 0
	case semver.MajorMinor(p) != "v0.0":
		return semver.Compare(semver.MajorMinor(t), semver.MajorMinor(p)) > 0
	default:
		return semver.Compare(t, p) > 0
	}
}

// releaseVersion is the version a tag releases, with any prerelease and build
// metadata dropped: v0.3.0-rc.1 is the 0.3.0 line reaching a consumer early,
// and the bump it carries is 0.3.0's.
func releaseVersion(tag string) string {
	canonical := semver.Canonical(tag)
	return strings.TrimSuffix(canonical, semver.Prerelease(canonical))
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
