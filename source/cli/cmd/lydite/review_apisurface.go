package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/reviewdecision"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// gateAPISurface is the label each component's API-surface verdict is
// rendered under, and the Gate on every finding the comparison produces.
const gateAPISurface = reviewdecision.GateAPISurface

// commandToolchains is how a comparison run inside a command provisions each
// component's toolchain and composes the environment its own code runs under:
// the same resolution `scan` and `test` do, with its diagnostics on the
// command's stderr.
type commandToolchains struct{ cmd *cobra.Command }

func (t commandToolchains) Ensure(ctx context.Context, dir string, cfg config.Config, components []component.Component) (toolchain.Envs, error) {
	return ensureToolchains(ctx, t.cmd, dir, cfg, componentUnits(components))
}

func (commandToolchains) CheckEnv(tc *toolchain.Env, c component.Component) []string {
	return childEnv(tc, c, runner.Invocation{})
}

// renderAPISurfaceRows renders what each comparison reviewdecision.Decide
// already folded into the decision. It runs no comparison and decides
// nothing: a declared break and a surface nothing could compare are
// disqualifications in the decision, and reach the report through it.
//
// declared names where the change declares a breaking API change, and is
// empty when nothing does. A component whose surface could not be compared
// gets no row of its own, because its disqualification already names it and
// why.
func renderAPISurfaceRows(report *ui.Report, base, declared string, results []reviewdecision.SurfaceComparison) {
	for _, res := range results {
		skipped := detailOf(reviewdecision.SkippedNote(res.Skipped))
		switch {
		case res.Uncomputable != "":
			continue
		case len(res.Findings) == 0:
			report.Add(ui.Row{
				Status: ui.StatusPass,
				Label:  gateAPISurface + "(" + res.Component + ")",
				Value:  "no incompatible change against " + shortSHA(base),
				Detail: skipped,
			})
		case declared != "":
			// Declared, so the referral is the verdict and this row is what
			// the reader needs to review: the break itself, not the claim
			// that there is one.
			report.Add(ui.Row{
				Status: ui.StatusRefer,
				Label:  gateAPISurface + "(" + res.Component + ")",
				Value:  fmt.Sprintf("%s, declared", incompatible(len(res.Findings))),
				Detail: append(capped(locate(res.Findings, res.Dir)), skipped...),
			})
		default:
			detail := append(capped(locate(res.Findings, res.Dir)), skipped...)
			detail = append(detail,
				"restore the API, or declare the break with a `!` in the type of this change's title or a commit, or a BREAKING CHANGE: footer")
			report.Add(ui.Row{
				Status: ui.StatusFail,
				Label:  gateAPISurface + "(" + res.Component + ")",
				Value:  fmt.Sprintf("%s, undeclared", incompatible(len(res.Findings))),
				Detail: detail,
			})
		}
	}
}

func incompatible(n int) string {
	return fmt.Sprintf("%d incompatible change(s) to the exported API", n)
}

// detailOf is one sentence as a row's detail, and no detail at all for an
// empty one — a row whose detail is a blank line says something happened and
// then does not say what.
func detailOf(note string) []string {
	if note == "" {
		return nil
	}
	return []string{note}
}

// locate renders each finding as the line a reader opens, rebasing its path
// onto the scan root the way every other producer's findings are: what
// internal/apisurface returns is relative to the tree it compared, which is
// the component's own directory.
func locate(findings []finding.Finding, dir string) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		// A symbol the loader could not place carries no path, and joining
		// one onto the component's directory would point at the directory as
		// though it were the file.
		if f.Path == "" {
			out = append(out, f.Message)
			continue
		}
		at := path.Join(dir, f.Path)
		// A symbol that is gone is located where the merge-base declared it,
		// and internal/apisurface says so in the finding's detail. The line
		// is named as the merge-base's, or a reader opens their own checkout
		// at a line that means nothing there.
		if len(f.Detail) > 0 {
			at = "merge-base " + at
		}
		out = append(out, fmt.Sprintf("%s:%d %s", at, f.Line, f.Message))
	}
	return out
}

// pullRequestTitle reads the title out of the webhook payload, and is empty
// wherever there is no payload to read.
//
// No flag is required and no environment is: a local review has no pull
// request, and the commits carry the declaration there. A payload that exists
// and cannot be read is warned about rather than fatal, for the same reason —
// the title can only add a referral, so failing the run over an unreadable
// one would turn an additive source into a blocker.
func pullRequestTitle(warn io.Writer, eventPath string) string {
	if eventPath == "" {
		eventPath = os.Getenv("GITHUB_EVENT_PATH")
	}
	if eventPath == "" {
		return ""
	}
	event, err := forge.LoadPullRequestEvent(eventPath)
	if err != nil {
		_, _ = fmt.Fprintf(warn, "warning: could not read the event at %s (%v) — a break declared only in the pull request title is not seen\n", eventPath, err)
		return ""
	}
	return event.PullRequest.Title
}
