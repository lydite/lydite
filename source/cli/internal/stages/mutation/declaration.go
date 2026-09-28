package mutationstages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// LoadDeclarationIn is the scan root, and the components this run is
// responsible for.
type LoadDeclarationIn struct {
	Dir string
	// Components names the components this run is responsible for, and is
	// empty for every declared component.
	Components []string
}

// LoadDeclarationOut is the configuration and the declaration in force, and
// this run's share of it.
type LoadDeclarationOut struct {
	Config config.Config
	File   component.File
	// Own is the components this run is responsible for, in declaration
	// order.
	Own []component.Component
	// Declared reports whether the declaration names any component at all.
	// Every later stage runs only when it holds.
	Declared bool
}

// LoadDeclaration reads the configuration, then the declaration, then this
// run's share of it.
//
// A declaration naming no component is not an error: Declared is false, and
// what that means for the report is the caller's to say.
func LoadDeclaration(_ context.Context, in LoadDeclarationIn) (LoadDeclarationOut, error) {
	cfg, err := config.Load(in.Dir)
	if err != nil {
		return LoadDeclarationOut{}, err
	}
	file, err := component.Load(in.Dir)
	if err != nil {
		return LoadDeclarationOut{}, err
	}
	own, err := file.Select(in.Components)
	if err != nil {
		return LoadDeclarationOut{}, err
	}
	return LoadDeclarationOut{Config: cfg, File: file, Own: own, Declared: len(file.Components) > 0}, nil
}

// ProvisionToolchainsIn is the components whose toolchains are provisioned.
type ProvisionToolchainsIn struct {
	Toolchains Toolchains
	Dir        string
	Config     config.Config
	// Own is the set this run may execute, rather than the set affected
	// selection narrows it to: a component deselected on one run and
	// selected on the next must not resolve a different toolchain.
	Own []component.Component
}

// ProvisionToolchainsOut is one environment per component.
type ProvisionToolchainsOut struct {
	Envs toolchain.Envs
}

// ProvisionToolchains provisions every toolchain this run's components need.
func ProvisionToolchains(ctx context.Context, in ProvisionToolchainsIn) (ProvisionToolchainsOut, error) {
	envs, err := in.Toolchains.Ensure(ctx, in.Dir, in.Config, in.Own)
	if err != nil {
		return ProvisionToolchainsOut{}, err
	}
	return ProvisionToolchainsOut{Envs: envs}, nil
}

// ResolveBaseIn is the two ways a caller names the base: a branch to take the
// merge-base against, or a revision outright.
type ResolveBaseIn struct {
	Dir        string
	BaseBranch string
	BaseSHA    string
}

// ResolveBaseOut is the commit this run's mutants come from the diff against.
type ResolveBaseOut struct {
	Base string
}

// ResolveBase resolves the base once for the whole run.
//
// Mutation is diff-scoped always, so the base is not something a run can do
// without: an unresolvable one is an error naming the fix rather than a run
// that quietly mutates nothing and reports a pass.
func ResolveBase(ctx context.Context, in ResolveBaseIn) (ResolveBaseOut, error) {
	base, err := resolveMutationBase(ctx, in.Dir, in.BaseBranch, in.BaseSHA)
	if err != nil {
		return ResolveBaseOut{}, err
	}
	return ResolveBaseOut{Base: base}, nil
}

// resolveMutationBase is the commit this run's mutants come from the diff
// against, resolved once for the whole run.
//
// Each flag fails with its own value named. The two resolutions fail for
// different reasons and have different fixes — a branch that could not be
// fetched or merged-base against, and a revision this checkout does not hold —
// so one message covering both would name a cause the caller can act on only
// half the time.
func resolveMutationBase(ctx context.Context, dir, baseBranch, baseSHA string) (string, error) {
	if baseSHA != "" {
		base, err := gitstate.ResolveRevision(ctx, dir, baseSHA)
		if err != nil {
			return "", fmt.Errorf("mutation is scoped to the change against %s %s, and it could not be resolved: %w",
				gitstate.BaseSHAFlag, baseSHA, err)
		}
		return base, nil
	}
	base, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
	if err != nil {
		return "", fmt.Errorf("mutation is scoped to the change against the merge-base, and it could not be resolved: %w"+
			"\n       a shallow checkout is the usual cause — fetch with depth 0", err)
	}
	return base, nil
}

// SelectAffectedIn is the run's own components, and whether to narrow them to
// those the change could have broken.
type SelectAffectedIn struct {
	// Only asks for the narrowing. Without it every component in Own is
	// selected and Affected is never called.
	Only     bool
	Affected AffectedFunc
	Dir      string
	File     component.File
	Own      []component.Component
	// Base is the base already resolved, never resolved again here: a
	// selection answering about a different range than the mutants were
	// generated against would report a component untouched while its own
	// mutants ran, or the reverse.
	Base string
}

// SelectAffectedOut is what this run mutates, and what it skipped.
type SelectAffectedOut struct {
	// Selected is the components to mutate. Own itself when Only is false.
	Selected []component.Component
	// Selection is the whole declaration's selection, zero when Only is
	// false.
	Selection affected.Result
	// Skipped is the components of Own the selection skipped, and Ordered is
	// Own, which a skipped component's row is interleaved by. Both are empty
	// when Only is false.
	Skipped []component.Component
	Ordered []component.Component
}

// SelectAffected narrows this run's components to those the change could have
// broken, when asked to, and passes them through unchanged when not — so a
// later stage reads Selected either way, never the output of a stage that did
// not run.
//
// Selection runs over the whole declaration, because a dependency edge reaches
// components another shard is responsible for; what this run mutates is its
// own share of that one answer.
func SelectAffected(ctx context.Context, in SelectAffectedIn) (SelectAffectedOut, error) {
	if !in.Only {
		return SelectAffectedOut{Selected: in.Own}, nil
	}
	res, err := in.Affected(ctx, in.Dir, in.File, in.Base)
	if err != nil {
		return SelectAffectedOut{}, err
	}
	return SelectAffectedOut{
		Selected:  intersect(res.Selected, in.Own),
		Selection: res,
		Skipped:   intersect(res.Skipped, in.Own),
		Ordered:   in.Own,
	}, nil
}

// intersect narrows cs to the components in own, keeping cs's order.
func intersect(cs []component.Component, own []component.Component) []component.Component {
	mine := make(map[string]bool, len(own))
	for _, c := range own {
		mine[c.Name] = true
	}
	out := make([]component.Component, 0, len(cs))
	for _, c := range cs {
		if mine[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// ScopeChangeIn is the change, and the components it is scoped for.
type ScopeChangeIn struct {
	Shape    Shape
	Dir      string
	Base     string
	Selected []component.Component
}

// ScopeChangeOut is every line the change added, and every file a worker
// directory would be copied from.
type ScopeChangeOut struct {
	// Changed is every line the diff added, repository-wide, keyed by a path
	// relative to the scan root.
	Changed map[string][]int
	// Files is every path git knows about under the scan root — tracked, plus
	// untracked ones it is not ignoring — and empty when no selected
	// component's mutants run in a worker directory.
	Files []string
}

// ScopeChange reads the change once for the whole run.
//
// Every changed line in one call, partitioned per component later: they all
// measure the same range, and asking git once per component is the same answer
// computed N times. The file listing is read only for a language whose mutants
// need a worker directory, so a repository of Go components pays no git walk
// for a copy it never makes.
func ScopeChange(ctx context.Context, in ScopeChangeIn) (ScopeChangeOut, error) {
	changed, err := coverage.ChangedLines(ctx, in.Dir, in.Base)
	if err != nil {
		return ScopeChangeOut{}, err
	}
	var files []string
	if needsWorktree(in.Shape, in.Selected) {
		if files, err = gitdiff.Tracked(ctx, in.Dir); err != nil {
			return ScopeChangeOut{}, err
		}
	}
	return ScopeChangeOut{Changed: changed, Files: files}, nil
}

// needsWorktree reports whether any selected component's mutants are run in a
// copy of its tree rather than through an overlay.
func needsWorktree(shape Shape, selected []component.Component) bool {
	for _, c := range selected {
		switch shape.Lang(c) {
		case runner.Rust, runner.TypeScript, runner.Python:
			return true
		}
	}
	return false
}
