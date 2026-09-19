package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/apisurface"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/declaration"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rustapisurface"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// gateAPISurface names the public-API diff wherever it is reported: the Gate
// on every finding it produces, and the label each component's verdict is
// rendered under.
const gateAPISurface = "api surface"

// surfaceComparison is one component's comparison, computed and separated
// from any decision so it can cross a job boundary: see computeAPISurfaces
// and renderAPISurfaceRows.
type surfaceComparison struct {
	Component    string            `json:"component"`
	Dir          string            `json:"dir"`
	Findings     []finding.Finding `json:"findings,omitempty"`
	Uncomputable string            `json:"uncomputable,omitempty"`
}

// computeAPISurfaces runs the comparison for every component that opted into
// api_surface, and only that: no report, no decision, no exemptions, no
// declaration. It is the one function in this file that runs a component's
// own code — building rustdoc for a Rust component's head tree runs that
// crate's own build.rs and proc-macros — and nothing past it does, which is
// why it returns data for a caller to render rather than rendering anything
// itself: the caller may be a job that must never hold a publishing
// credential, or one that must never run this comparison again to get it.
func computeAPISurfaces(ctx context.Context, cmd *cobra.Command, dir, base string) ([]surfaceComparison, error) {
	file, err := component.Load(dir)
	if err != nil {
		return nil, err
	}
	var opted []component.Component
	for _, c := range file.Components {
		if c.APISurface != nil {
			opted = append(opted, c)
		}
	}
	// A repository where nothing opted in pays none of what follows, and says
	// nothing about it. Nothing was asked for, so there is no gate here that
	// could not run.
	if len(opted) == 0 {
		return nil, nil
	}

	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	// The same resolution `scan` and `test` do, for the same reason: the
	// module is loaded by the `go` its own directory declares, not by
	// whichever one the PATH leads to.
	envs, err := ensureToolchains(ctx, cmd, dir, cfg, componentUnits(opted))
	if err != nil {
		return nil, err
	}

	root, remove, err := baseWorktree(ctx, dir, base)
	if err != nil {
		// No tree, so no component's surface can be compared. Each one that
		// asked says so under its own name, since a component missing from
		// the result is indistinguishable from one that was compared and
		// found clean.
		results := make([]surfaceComparison, 0, len(opted))
		for _, c := range opted {
			results = append(results, surfaceComparison{Component: c.Name, Dir: c.Dir, Uncomputable: err.Error()})
		}
		return results, nil
	}
	defer remove()

	results := make([]surfaceComparison, 0, len(opted))
	for _, c := range opted {
		findings, uncomputable := compareSurface(ctx, cmd, c, root, dir, envs)
		results = append(results, surfaceComparison{Component: c.Name, Dir: c.Dir, Findings: findings, Uncomputable: uncomputable})
	}
	return results, nil
}

// uncomputableSurfaces reports every component that opted into api_surface as
// uncomputable for the given reason, without running any comparison.
//
// Used when the document review compare wrote could not be read at all: a
// missing or corrupted artifact is not evidence the change is clean, and
// returning early with a plain error here would exit 1 before any
// disqualification is added — a workflow step that treats exit codes at or
// under 2 as an answer rather than a malfunction would then publish nothing
// at all, which is worse than a referral it can at least act on.
func uncomputableSurfaces(dir, reason string) ([]surfaceComparison, error) {
	file, err := component.Load(dir)
	if err != nil {
		return nil, err
	}
	var results []surfaceComparison
	for _, c := range file.Components {
		if c.APISurface != nil {
			results = append(results, surfaceComparison{Component: c.Name, Dir: c.Dir, Uncomputable: reason})
		}
	}
	return results, nil
}

// renderAPISurfaceRows decides what results computeAPISurfaces already made
// mean, and folds the answer into the report and into the decision. It runs
// no comparison and executes no component's own code: reading the
// declaration is the only thing here that touches the change under review,
// and it reads text — a title, a commit message — never runs any of it.
//
// Three verdicts, and which one a break gets is decided by the declaration
// alone (see docs/adr/0040):
//
//   - an undeclared break fails, because the author clears it by not breaking
//     the API or by declaring the break, and both are work they can do;
//   - a declared break refers, because every breaking change should reach a
//     person;
//   - a surface that could not be compared refers, because review genuinely
//     cannot tell a break from no break and neither pass nor fail is true.
//
// The declaration is read once and honoured on its own: a change that
// declares a break is referred even where no component opted in and nothing
// was compared. That is the one-way ratchet the marker is only safe under —
// the claim can add a referral and can never remove one, so spelling `feat!:`
// rather than `feat:` never makes anything greener.
func renderAPISurfaceRows(ctx context.Context, cmd *cobra.Command, report *ui.Report, d *referral.Decision, dir, base, eventPath string, results []surfaceComparison) {
	where := breakDeclaration(ctx, cmd.ErrOrStderr(), dir, base, eventPath)
	if where != "" {
		refer(d, referral.Disqualification{
			Kind:     referral.DisqualificationAPIBreakDeclared,
			Evidence: "declared in " + where,
		})
	}

	for _, res := range results {
		c := component.Component{Name: res.Component, Dir: res.Dir}
		switch {
		case res.Uncomputable != "":
			referUncomputable(d, c, res.Uncomputable)
		case len(res.Findings) == 0:
			report.Add(ui.Row{
				Status: ui.StatusPass,
				Label:  gateAPISurface + "(" + c.Name + ")",
				Value:  "no incompatible change against " + shortSHA(base),
			})
		case where != "":
			// Declared, so the referral above is the verdict and this row is
			// what the reader needs to review: the break itself, not the
			// claim that there is one.
			report.Add(ui.Row{
				Status: ui.StatusRefer,
				Label:  gateAPISurface + "(" + c.Name + ")",
				Value:  fmt.Sprintf("%s, declared", incompatible(len(res.Findings))),
				Detail: capped(locate(res.Findings, c.Dir)),
			})
		default:
			report.Add(ui.Row{
				Status: ui.StatusFail,
				Label:  gateAPISurface + "(" + c.Name + ")",
				Value:  fmt.Sprintf("%s, undeclared", incompatible(len(res.Findings))),
				Detail: append(capped(locate(res.Findings, c.Dir)),
					"restore the API, or declare the break with a `!` in the type of this change's title or a commit, or a BREAKING CHANGE: footer"),
			})
		}
	}
}

// compareSurface compares one component's public API against the merge-base
// with the comparison its language has, and names what stopped it when the
// comparison could not be made at all.
//
// Both trees are prepared here rather than in either comparison package: the
// merge-base is materialised with git, and neither internal/apisurface nor
// internal/rustapisurface does any — each takes two directories and returns
// findings.
//
// The two languages differ only in which tool makes the comparison. What a
// break means, and what a surface that could not be compared means, is one rule
// for both: see addAPISurfaceRows.
func compareSurface(ctx context.Context, cmd *cobra.Command, c component.Component, root, dir string, envs toolchain.Envs) ([]finding.Finding, string) {
	base := filepath.Join(root, filepath.FromSlash(c.Dir))
	head := filepath.Join(dir, filepath.FromSlash(c.Dir))
	tc := envs.For(c.Name)
	switch langOf(c) {
	case runner.Go:
		findings, err := apisurface.Compare(base, head, gateAPISurface, c.Name, tc.Environ())
		switch {
		case errors.Is(err, apisurface.ErrModulePathChanged):
			return nil, "the module path changed between the merge-base and this change"
		case err != nil:
			return nil, err.Error()
		}
		return findings, ""
	case runner.Rust:
		// The two environments every other Rust check runs under, composed the
		// same way: the component's own toolchain and declaration build the two
		// trees, and lydite's own provisions the pinned cargo-semver-checks.
		res := rustapisurface.Compare(ctx, rustapisurface.Request{
			BaseDir:   base,
			HeadDir:   head,
			Gate:      gateAPISurface,
			Component: c.Name,
			Env: executil.Env{
				Check:   childEnv(tc, c, runner.Invocation{}),
				Install: tc.Environ(),
			},
			Progress: cmd.ErrOrStderr(),
		})
		if res.Outcome == rustapisurface.Unmeasurable {
			return nil, res.Reason
		}
		return res.Findings, ""
	default:
		// component.Load refuses api_surface on every other language, so a
		// component reaching here has none lydite can compare. Said out loud,
		// because a comparison that never happened must not render as one that
		// found nothing.
		return nil, "no public-API comparison exists for this component's language"
	}
}

// refer adds a disqualification and states the verdict that follows from it.
//
// Both, together, always. A disqualification appended without Referred set
// would be rendered and then have no effect at all on a change an exemption
// covered — which is the one case where adding it mattered.
func refer(d *referral.Decision, dq referral.Disqualification) {
	d.Disqualifications = append(d.Disqualifications, dq)
	d.Referred = true
}

// referUncomputable refers a component whose surface could not be compared,
// naming what stopped it.
func referUncomputable(d *referral.Decision, c component.Component, reason string) {
	refer(d, referral.Disqualification{
		Kind:     referral.DisqualificationAPISurfaceUncomputable,
		Path:     c.Dir,
		Evidence: c.Name + ": " + reason,
	})
}

func incompatible(n int) string {
	return fmt.Sprintf("%d incompatible change(s) to the exported API", n)
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

// breakDeclaration names where this change declares a breaking API change, or
// returns empty when nothing does.
//
// Both sources are read, because the two answer different questions and
// neither subsumes the other. Squash merge makes the title the commit that
// lands, so a break declared only in a commit about to be squashed away
// leaves no marker in the history; locally there is no pull request and no
// title, and the commits are all there is.
func breakDeclaration(ctx context.Context, warn io.Writer, dir, base, eventPath string) string {
	if title := pullRequestTitle(warn, eventPath); declaration.Declared(title) {
		return "the pull request title"
	}
	messages, err := gitstate.CommitMessages(ctx, dir, base, "HEAD")
	if err != nil {
		// Said out loud rather than swallowed: unread commits can only ever
		// under-refer, and a reader who declared a break in one of them would
		// otherwise see no sign that the declaration was never looked at.
		_, _ = fmt.Fprintf(warn, "warning: could not read the commits in %s..HEAD (%v) — a break declared only in one of them is not seen\n", shortSHA(base), err)
		return ""
	}
	for _, message := range messages {
		if declaration.Declared(message) {
			return "a commit in " + shortSHA(base) + "..HEAD"
		}
	}
	return ""
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

// baseWorktree checks the base commit out into a throwaway worktree and
// returns the scan root inside it, with the removal the caller must run.
//
// A real tree on disk, because `go/packages` loads a module by running the Go
// tool over one and `cargo-semver-checks` takes its baseline as a checked-out
// source root it builds rustdoc from; `git show <base>:<path>` cannot supply
// either.
//
// The scan root may sit below the repository root, so the worktree is entered
// at the same prefix --dir sits at. Comparing at the worktree root instead
// would read a different repository's declaration, or none at all.
func baseWorktree(ctx context.Context, dir, base string) (string, func(), error) {
	prefix, err := referral.RootRelative(ctx, dir)
	if err != nil {
		return "", nil, fmt.Errorf("locating the scan root inside the repository: %w", err)
	}
	tmp, err := os.MkdirTemp("", "lydite-apisurface-*")
	if err != nil {
		return "", nil, err
	}
	if r := executil.RunQuiet(ctx, dir, "git", "worktree", "add", "--detach", tmp, base); !r.Ok() {
		_ = os.RemoveAll(tmp)
		return "", nil, fmt.Errorf("checking out %s to compare its API: %w", shortSHA(base), r.Err)
	}
	// A context of its own: the run's may already be cancelled, and an
	// interrupt that kills the removal while the directory is deleted anyway
	// leaves a registered worktree pointing at nothing, which every later
	// `git worktree add` and `git worktree list` in that repository trips
	// over until someone prunes it.
	remove := func() {
		_ = executil.RunQuiet(context.WithoutCancel(ctx), dir, "git", "worktree", "remove", "--force", tmp)
		_ = os.RemoveAll(tmp)
	}
	return filepath.Join(tmp, filepath.FromSlash(prefix)), remove, nil
}
