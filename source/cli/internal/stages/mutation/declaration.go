package mutationstages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/treedigest"
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
	// StateDir is the resume state root, and empty when resume is off. A root
	// inside Dir is kept out of every listing this stage makes.
	StateDir string
	// Diagnostics is where a failure touching the resume state is named, as it
	// arises. Nil discards.
	Diagnostics io.Writer
}

// ScopeChangeOut is every line the change added, every file a worker
// directory would be copied from, and the digest of the tree.
type ScopeChangeOut struct {
	// Changed is every line the diff added, repository-wide, keyed by a path
	// relative to the scan root.
	Changed map[string][]int
	// Files is every path git knows about under the scan root — tracked, plus
	// untracked ones it is not ignoring — minus the state root, and empty when
	// no selected component's mutants run in a worker directory.
	Files []string
	// TreeDigest is the digest of every path git knows about under the scan
	// root, minus the state root, and empty when resume is off.
	TreeDigest string
}

// ScopeChange reads the change once for the whole run.
//
// Every changed line in one call, partitioned per component later: they all
// measure the same range, and asking git once per component is the same answer
// computed N times. The file listing is read once, when a language whose
// mutants need a worker directory is selected or when resume needs the tree
// digest, so a Go-only run with resume off pays no git walk for a copy it
// never makes.
//
// The state root is dropped from the listing before it is digested or handed
// on: its files are appended to on every run, so a digest that covered them
// would change each time and nothing recorded would ever be reused. A
// `.gitignore` ignoring everything is written into it, so git does not list
// its files as untracked either.
func ScopeChange(ctx context.Context, in ScopeChangeIn) (ScopeChangeOut, error) {
	changed, err := coverage.ChangedLines(ctx, in.Dir, in.Base)
	if err != nil {
		return ScopeChangeOut{}, err
	}
	out := ScopeChangeOut{Changed: changed}
	diagnostics := in.Diagnostics
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	worktree := needsWorktree(in.Shape, in.Selected)
	resume := in.StateDir != ""
	if resume {
		if err := ignoreState(in.StateDir); err != nil {
			warnStateOff(diagnostics, err)
			resume = false
		}
	}
	if !worktree && !resume {
		return out, nil
	}
	files, err := gitdiff.Tracked(ctx, in.Dir)
	if err != nil {
		if worktree {
			return ScopeChangeOut{}, err
		}
		warnStateOff(diagnostics, err)
		return out, nil
	}
	files = withoutState(files, in.Dir, in.StateDir)
	if worktree {
		out.Files = files
	}
	if resume {
		digest, err := digestTree(in.Dir, files)
		if err != nil {
			warnStateOff(diagnostics, err)
			return out, nil
		}
		out.TreeDigest = digest
	}
	return out, nil
}

// digestTree is the digest of the regular files among files, relative to dir.
func digestTree(dir string, files []string) (string, error) {
	present, err := regularFiles(dir, files)
	if err != nil {
		return "", err
	}
	return treedigest.Digest(dir, present)
}

// warnStateOff says resume is off for the run. The state is a cache, so a
// failure touching it costs the reuse of recorded verdicts and nothing else.
func warnStateOff(w io.Writer, err error) {
	_, _ = fmt.Fprintf(w, "warning: mutation state is off: %v; every mutant is measured\n", err)
}

// regularFiles keeps the entries of files, relative to dir, that are regular
// files, or symlinks to one. The listing names a tracked file deleted from the
// worktree, a submodule's directory and a symlink to a directory, none of which
// can be hashed, and the digest is a cache key that must not fail a run over a
// dirty tree. A deleted file still changes the digest, by dropping out of the
// list.
func regularFiles(dir string, files []string) ([]string, error) {
	kept := make([]string, 0, len(files))
	for _, f := range files {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

// ignoreState creates the state root and writes into it a `.gitignore`
// ignoring everything, itself included, so the state is never part of the tree
// it describes. An existing one is left as it is.
func ignoreState(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("creating the mutation state directory: %w", err)
	}
	path := filepath.Join(stateDir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte("*\n"), 0o600); err != nil {
		return fmt.Errorf("ignoring the mutation state directory: %w", err)
	}
	return nil
}

// withoutState drops every path under stateDir from files, which are relative
// to dir. A state root outside dir, or none, leaves files as they are.
func withoutState(files []string, dir, stateDir string) []string {
	if stateDir == "" {
		return files
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return files
	}
	absState, err := filepath.Abs(stateDir)
	if err != nil {
		return files
	}
	rel, err := filepath.Rel(absDir, absState)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return files
	}
	prefix := filepath.ToSlash(rel) + "/"
	kept := make([]string, 0, len(files))
	for _, f := range files {
		if !strings.HasPrefix(f, prefix) {
			kept = append(kept, f)
		}
	}
	return kept
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
