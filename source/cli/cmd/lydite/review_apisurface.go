package main

import (
	"context"
	"fmt"
	"path"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
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

// apiSurfaceRow renders one comparison reviewdecision.Decide already decided.
// It runs no comparison and decides nothing: the outcome's status says whether
// the surface held, broke under a declaration, or broke without one, and a
// surface nothing could compare is a disqualification that names the
// component and why, with no row of its own here.
func apiSurfaceRow(o reviewdecision.Outcome, status ui.Status) ui.Row {
	res := o.Surface
	label := gateAPISurface + "(" + res.Component + ")"
	skipped := detailOf(reviewdecision.SkippedNote(res.Skipped))
	switch o.Status {
	case reviewdecision.StatusPass:
		return ui.Row{
			Status: status,
			Label:  label,
			Value:  "no incompatible change against " + shortSHA(o.Base),
			Detail: skipped,
		}
	case reviewdecision.StatusRefer:
		// Declared, so the referral is the verdict and this row is what the
		// reader needs to review: the break itself, not the claim that there
		// is one.
		return ui.Row{
			Status: status,
			Label:  label,
			Value:  fmt.Sprintf("%s, declared", incompatible(len(res.Findings))),
			Detail: append(capped(locate(res.Findings, res.Dir)), skipped...),
		}
	default:
		detail := append(capped(locate(res.Findings, res.Dir)), skipped...)
		detail = append(detail,
			"restore the API, or declare the break with a `!` in the type of this change's title or a commit, or a BREAKING CHANGE: footer")
		return ui.Row{
			Status: status,
			Label:  label,
			Value:  fmt.Sprintf("%s, undeclared", incompatible(len(res.Findings))),
			Detail: detail,
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

