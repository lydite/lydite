package reviewdecision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/apisurface"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rustapisurface"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/tsapisurface"
)

// GateAPISurface names the public-API diff wherever it is reported: the Gate
// on every finding it produces, and the label each component's verdict is
// rendered under.
const GateAPISurface = "api surface"

// SurfaceComparison is one component's comparison, computed and separated
// from any decision so it can cross a job boundary: see CompareSurfaces and
// Decide.
type SurfaceComparison struct {
	Component    string            `json:"component"`
	Dir          string            `json:"dir"`
	Findings     []finding.Finding `json:"findings,omitempty"`
	Uncomputable string            `json:"uncomputable,omitempty"`
	// Skipped names every part of the component the comparison left out
	// because it declares no surface at all — a TypeScript package naming no
	// entry point. It is rendered rather than dropped: a package nothing
	// compared is otherwise indistinguishable from one compared and found
	// clean. Like Findings and Uncomputable it crosses the job boundary as
	// text the compared code could have written, and like them it can only
	// ever add a line to the report, never remove a gate.
	Skipped []string `json:"skipped,omitempty"`
}

// SurfaceDocument is what `review compare` writes and `review --surfaces`
// reads: the merge-base that run resolved, and each opted-in component's raw
// comparison result.
//
// The base travels with the results rather than being re-resolved by the
// reader: origin's default branch can move between two independent
// resolutions of "auto", and a decision computed against a different base
// than the one the comparisons actually ran against would be answering a
// question nobody asked.
type SurfaceDocument struct {
	Base    string              `json:"base"`
	Results []SurfaceComparison `json:"results"`
}

// WriteSurfaces records CompareSurfaces's result, for a later, separate
// invocation to read with ReadSurfaces.
func WriteSurfaces(path, base string, results []SurfaceComparison) error {
	f, err := os.Create(path) // #nosec G304 -- a workflow's own artifact path, not attacker-controlled
	if err != nil {
		return fmt.Errorf("writing the surface comparisons: %w", err)
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(SurfaceDocument{Base: base, Results: results})
}

// ReadSurfaces is WriteSurfaces's inverse.
func ReadSurfaces(path string) (SurfaceDocument, error) {
	f, err := os.Open(path) // #nosec G304 -- a workflow's own artifact path, not attacker-controlled
	if err != nil {
		return SurfaceDocument{}, fmt.Errorf("reading the surface comparisons: %w", err)
	}
	defer func() { _ = f.Close() }()
	var doc SurfaceDocument
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return SurfaceDocument{}, fmt.Errorf("reading the surface comparisons: %w", err)
	}
	if doc.Base == "" {
		return SurfaceDocument{}, fmt.Errorf("the surface document names no base commit")
	}
	return doc, nil
}

// Toolchains is what running a comparison in this process needs from the
// process that runs it: the toolchain each component's language resolves to,
// and the environment a component's own code is run under.
type Toolchains interface {
	// Ensure provisions the toolchain every one of components needs, at the
	// version its own directory declares, under cfg's overrides.
	Ensure(ctx context.Context, dir string, cfg config.Config, components []component.Component) (toolchain.Envs, error)
	// CheckEnv is the environment c's own code is built and run under,
	// composed over the toolchain tc resolved for it.
	CheckEnv(tc *toolchain.Env, c component.Component) []string
}

// Surfaces is every opted-in component's comparison, read from the document a
// separate `review compare` wrote when document names one, and made here
// through CompareSurfaces when it does not.
//
// A document is never taken on its own word: reconcileSurfaces checks the base
// it names against the base this run resolved, and requires a result for every
// component this tree says opted in. A document that cannot be read at all
// makes every opted-in component uncomputable rather than failing the run: a
// missing or corrupted artifact is not evidence the change is clean, and an
// error here would exit 1 before any disqualification is added — a workflow
// step that treats exit codes at or under 2 as an answer rather than a
// malfunction would then publish nothing at all, which is worse than a
// referral it can at least act on.
func Surfaces(ctx context.Context, dir, base, document string, guardCredential bool, tc Toolchains, progress io.Writer) ([]SurfaceComparison, error) {
	if document == "" {
		return CompareSurfaces(ctx, dir, base, guardCredential, tc, progress)
	}
	doc, err := ReadSurfaces(document)
	if err != nil {
		return uncomputableSurfaces(dir, "the comparison document could not be read: "+err.Error())
	}
	return reconcileSurfaces(dir, base, doc)
}

// CompareSurfaces runs the comparison for every component that opted into
// api_surface, and only that: no report, no decision, no exemptions, no
// declaration. It is the one function in this package that runs a
// component's own code — building rustdoc for a Rust component's head tree
// runs that crate's own build.rs and proc-macros, and installing a TypeScript
// component runs both trees' lifecycle scripts — and nothing past it does,
// which is why it returns data rather than a decision: the caller may be a
// job that must never hold a publishing credential, or one that must never
// run this comparison again to get it.
//
// guardCredential refuses to run the comparisons that execute the tree under
// review — see untrustedBuild — reporting each uncomputable instead: a caller
// sets it exactly when its process holds a credential, because
// executil.RunQuietIsolatedEnv keeps that credential out of the comparison's
// own child environment but not out of this process's — a same-user
// descendant can still reach it another way (see
// agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md).
//
// progress is where a comparison's own tool reports what it is doing.
func CompareSurfaces(ctx context.Context, dir, base string, guardCredential bool, tc Toolchains, progress io.Writer) ([]SurfaceComparison, error) {
	opted, err := optedInComponents(dir)
	if err != nil {
		return nil, err
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
	envs, err := tc.Ensure(ctx, dir, cfg, opted)
	if err != nil {
		return nil, err
	}

	root, remove, err := baseWorktree(ctx, dir, base)
	if err != nil {
		// No tree, so no component's surface can be compared. Each one that
		// asked says so under its own name, since a component missing from
		// the result is indistinguishable from one that was compared and
		// found clean.
		results := make([]SurfaceComparison, 0, len(opted))
		for _, c := range opted {
			results = append(results, SurfaceComparison{Component: c.Name, Dir: c.Dir, Uncomputable: err.Error()})
		}
		return results, nil
	}
	defer remove()

	results := make([]SurfaceComparison, 0, len(opted))
	for _, c := range opted {
		if what := untrustedBuild(c); guardCredential && what != "" {
			results = append(results, SurfaceComparison{
				Component: c.Name, Dir: c.Dir,
				Uncomputable: "this component's comparison runs " + what + ", which must not happen in the same process that is about to publish with a credential — run `review compare` and `review --surfaces` as two separate invocations instead",
			})
			continue
		}
		findings, skipped, uncomputable := compareSurface(ctx, c, root, dir, envs, tc, progress)
		results = append(results, SurfaceComparison{Component: c.Name, Dir: c.Dir, Findings: findings, Skipped: skipped, Uncomputable: uncomputable})
	}
	return results, nil
}

// untrustedBuild names what a component's comparison executes out of the tree
// under review, and is empty for one that executes none of it.
//
// Not every comparison does. Go's loads the two trees with the Go tool and
// compiles nothing the change wrote, so a Go component's surface can be
// compared beside a credential. Rust's builds rustdoc, which runs the head
// tree's own build.rs and proc-macros, and TypeScript's installs and builds
// both trees, which runs their lifecycle scripts and their own compiler
// configuration. Both put the change's code in a child of this process, which
// is the thing guardCredential exists to keep out of a publishing job.
func untrustedBuild(c component.Component) string {
	switch c.Lang() {
	case runner.Rust:
		return "the head tree's own build.rs and proc-macros"
	case runner.TypeScript:
		return "both trees' own npm lifecycle scripts and build"
	default:
		return ""
	}
}

// optedInComponents is every component this tree declares api_surface for.
func optedInComponents(dir string) ([]component.Component, error) {
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
	return opted, nil
}

// uncomputableSurfaces reports every component that opted into api_surface as
// uncomputable for the given reason, without running any comparison.
func uncomputableSurfaces(dir, reason string) ([]SurfaceComparison, error) {
	opted, err := optedInComponents(dir)
	if err != nil {
		return nil, err
	}
	var results []SurfaceComparison
	for _, c := range opted {
		results = append(results, SurfaceComparison{Component: c.Name, Dir: c.Dir, Uncomputable: reason})
	}
	return results, nil
}

// reconcileSurfaces validates a comparison document against what this
// invocation independently determines to be true, rather than trusting
// either the base or the result set the document itself claims.
//
// The document crosses from a job that ran the change's own code — a Rust
// component's build.rs, a proc-macro — to one that is about to publish with
// a credential, so both fields it carries are exactly what that code could
// forge: a base equal to HEAD makes the diff this run reads look empty, and
// an empty result set skips every gate silently. Neither is accepted at
// face value. base is what this invocation resolved itself, never the
// document's own claim, and a mismatch refers every opted-in component
// rather than trusting a comparison that ran against some other commit. The
// opted-in component list is read fresh from this tree, and any one of them
// missing from the document's results — forged, or simply never written —
// is its own uncomputable row rather than a silent absence a reader cannot
// tell apart from "found nothing".
func reconcileSurfaces(dir, base string, doc SurfaceDocument) ([]SurfaceComparison, error) {
	opted, err := optedInComponents(dir)
	if err != nil {
		return nil, err
	}
	if doc.Base != base {
		return uncomputableSurfaces(dir, fmt.Sprintf(
			"the comparison document names %s as its base, but this run resolved %s — a comparison against a different commit cannot be trusted",
			short(doc.Base), short(base)))
	}
	byName := make(map[string]SurfaceComparison, len(doc.Results))
	for _, r := range doc.Results {
		byName[r.Component] = r
	}
	results := make([]SurfaceComparison, 0, len(opted))
	for _, c := range opted {
		r, ok := byName[c.Name]
		if !ok {
			r = SurfaceComparison{Uncomputable: "the comparison document carries no result for this component"}
		}
		// Component and Dir come from this tree's own component list, never
		// from the document: they name where a finding's path is rebased
		// and rendered, and the document is exactly what a malicious build
		// script could have written.
		r.Component, r.Dir = c.Name, c.Dir
		results = append(results, r)
	}
	return results, nil
}

// compareSurface compares one component's public API against the merge-base
// with the comparison its language has, and names what stopped it when the
// comparison could not be made at all.
//
// Both trees are prepared here rather than in any comparison package: the
// merge-base is materialised with git, and none of internal/apisurface,
// internal/rustapisurface or internal/tsapisurface does any — each takes two
// directories and returns findings.
//
// The second return is every part of the component that declares no surface at
// all, which is neither a break nor a failure to compare one but has to reach a
// reader all the same: see SurfaceComparison.Skipped.
//
// The three languages differ only in which tool makes the comparison. What a
// break means, and what a surface that could not be compared means, is one rule
// for all of them: see referSurfaces.
func compareSurface(ctx context.Context, c component.Component, root, dir string, envs toolchain.Envs, tcs Toolchains, progress io.Writer) ([]finding.Finding, []string, string) {
	base := filepath.Join(root, filepath.FromSlash(c.Dir))
	head := filepath.Join(dir, filepath.FromSlash(c.Dir))
	tc := envs.For(c.Name)
	switch c.Lang() {
	case runner.Go:
		findings, err := apisurface.Compare(base, head, GateAPISurface, c.Name, tc.Environ())
		switch {
		case errors.Is(err, apisurface.ErrModulePathChanged):
			return nil, nil, "the module path changed between the merge-base and this change"
		case err != nil:
			return nil, nil, err.Error()
		}
		return findings, nil, ""
	case runner.Rust:
		// The two environments every other Rust check runs under, composed the
		// same way: the component's own toolchain and declaration build the two
		// trees, and lydite's own provisions the pinned cargo-semver-checks.
		res := rustapisurface.Compare(ctx, rustapisurface.Request{
			BaseDir:   base,
			HeadDir:   head,
			Gate:      GateAPISurface,
			Component: c.Name,
			Env: executil.Env{
				Check:   tcs.CheckEnv(tc, c),
				Install: tc.Environ(),
			},
			Progress: progress,
		})
		if res.Outcome == rustapisurface.Unmeasurable {
			return nil, nil, res.Reason
		}
		return res.Findings, nil, ""
	case runner.TypeScript:
		// The same two environments, composed the same way: the component's own
		// toolchain and declaration install and build the two trees, and
		// lydite's own provisions the pinned api-extractor. The isolation that
		// keeps this process's own environment out of the install is
		// internal/tsapisurface's, for the reason its package doc gives.
		res := tsapisurface.Compare(ctx, tsapisurface.Request{
			BaseDir:   base,
			HeadDir:   head,
			Gate:      GateAPISurface,
			Component: c.Name,
			Env: executil.Env{
				Check:   tcs.CheckEnv(tc, c),
				Install: tc.Environ(),
			},
			Progress: progress,
		})
		if res.Outcome == tsapisurface.Unmeasurable {
			return nil, res.Skipped, res.Reason
		}
		return res.Findings, res.Skipped, ""
	default:
		// component.Load refuses api_surface on a component that declares no
		// language, so a component reaching here has nothing lydite can
		// compare. Said out loud, because a comparison that never happened must
		// not render as one that found nothing.
		return nil, nil, "no public-API comparison exists for a component that declares no language"
	}
}

// SkippedNote names what a comparison left out, and is empty when it left out
// nothing.
//
// A package naming no entry point declares no surface a consumer can reach, so
// leaving it out is the right answer rather than a failure. It is still said
// out loud: a component whose every package was skipped reads exactly like one
// compared and found clean, and a reader told nothing cannot tell which they
// are looking at.
func SkippedNote(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	return "not compared, because it names no entry point: " + strings.Join(skipped, ", ")
}

// withNote puts a note after a reason, and is the reason alone when there is none.
func withNote(reason, note string) string {
	if note == "" {
		return reason
	}
	return reason + "; " + note
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
// at the same prefix the scan root sits at. Comparing at the worktree root
// instead would read a different repository's declaration, or none at all.
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
		return "", nil, fmt.Errorf("checking out %s to compare its API: %w", short(base), r.Err)
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
