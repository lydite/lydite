package run

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/test/measure"
	"lydite/lydite/internal/toolchain"
)

// ChildEnv is the environment one of a component's commands runs with: the
// invocation's own pinned-tool directories ahead of the component's resolved
// toolchain on PATH, then the component's declared variables, and the
// toolchain's last of all.
//
// One function, and one PATH entry, because a child's environment is a flat
// list where the last occurrence of a key wins — two callers each prepending
// their own directories produce two PATH entries and one of them is silently
// dropped, which is invisible in argv and in every log. The pinned tool goes
// first on PATH because it is the more specific of the two.
//
// **The toolchain's variables go last, so they win**, and the ordering is the
// whole of the rule: a component declaring `GOTOOLCHAIN: auto` would otherwise
// cancel the `GOTOOLCHAIN=local` pinAmbientGo exists to set, reinstating the
// `go install` downgrade that made govulncheck reject the source it was
// pointed at — with every run still green. A repository states how its own
// code builds; lydite states which toolchain builds it. Reordering these
// arguments is that regression, so the inline comment at the return says it
// again where the change would be made.
//
// A PATH the component declares is folded into that one entry rather than set
// as a variable of its own — it is the single key a component cannot simply
// state, because the composed entry would always be the later of the two and
// would win outright.
//
// It is appended **after** the inherited PATH, and that is the security
// boundary rather than a preference. lydite resolves a program against the
// environment it hands the child, so a declared directory placed ahead of the
// inherited one would let `.lydite/components.yml` decide which `go`, `cargo`,
// `npm` or `sh` lydite itself launches: a repository shipping `ci-bin/go` and
// declaring `env: {PATH: ci-bin}` would have `lydite scan` run that binary to
// install gosec, on a runner where the ambient toolchain was already verified.
// A component may extend the path its suite runs with; it may not choose the
// toolchain lydite runs. Ordering keeps the useful case — a helper that exists
// nowhere else is still found — and removes the shadowing one.
func ChildEnv(tc *toolchain.Env, c component.Component, inv runner.Invocation) []string {
	dirs := append([]string{}, inv.PathDirs...)
	if tc != nil {
		dirs = append(dirs, tc.PathDirs...)
	}
	declared, vars := SplitPath(Env(c))
	if tc == nil {
		return toolchain.Compose(dirs, declared, vars)
	}
	// tc.Vars last, so they win: see the ordering rule above. Swapping these
	// two lets a declared GOTOOLCHAIN cancel the pin, and nothing goes red.
	return toolchain.Compose(dirs, declared, vars, tc.Vars)
}

// SplitPath separates a PATH a component declared into its directories,
// returning the remaining variables untouched. The last PATH wins, matching
// how a process reads duplicate keys out of its own environment.
func SplitPath(declared []string) (dirs, vars []string) {
	path := ""
	for _, kv := range declared {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
			continue
		}
		vars = append(vars, kv)
	}
	if path == "" {
		return nil, vars
	}
	return filepath.SplitList(path), vars
}

// Env renders a component's declared environment as the "KEY=value" entries
// executil appends to the child's own.
func Env(c component.Component) []string {
	if len(c.Env) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Env))
	for k, v := range c.Env {
		out = append(out, k+"="+v)
	}
	// Sorted, so two runs of the same declaration hand the child the same
	// environment in the same order: a map's iteration order is not one a
	// failure can be reproduced from.
	sort.Strings(out)
	return out
}

// ComponentUnits is what each of these components needs a toolchain for, in
// declaration order.
//
// The caller says which components, and it is the set that run may execute
// rather than the set affected selection ended up choosing: a component
// deselected on one run and selected on the next must not resolve a different
// toolchain, and selection is not known until after the git walk this feeds.
// A shard's set is its own, since it can never execute a component outside it
// and materialising a rustup channel for one is a download nothing uses.
//
// A component declaring its own command implies no language: its lang: names
// what it is scanned as, and Lang — the language a suite runs in — stays empty
// without a runner. One whose own directory under root holds a package.json is
// resolved through the workspace's Node toolchain instead, as a NodeCommand
// unit, because the command it runs installs and runs through that Node and
// its pinned package manager. A command without one needs nothing, and neither
// does a component declaring no suite.
func ComponentUnits(root string, components []component.Component) []toolchain.Unit {
	var out []toolchain.Unit
	for _, c := range components {
		if lang := measure.LangOf(c); lang != "" {
			out = append(out, toolchain.Unit{Name: c.Name, Lang: lang, Dir: c.Dir})
			continue
		}
		if len(c.Command) > 0 && hasPackageJSON(root, c.Dir) {
			out = append(out, toolchain.Unit{Name: c.Name, Dir: c.Dir, NodeCommand: true})
		}
	}
	return out
}

// hasPackageJSON reports whether a component's own directory, relative to
// root, holds a package.json.
func hasPackageJSON(root, dir string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "package.json"))
	return err == nil && !info.IsDir()
}

// EnsureToolchains makes each unit's language toolchain available at the
// version its own directory declares, and returns the environment every
// command run for that component needs.
//
// Called by `scan`, `test` and every other command that runs a component's
// tools, and in each case before any tool runs. Wiring it into the command
// entry points rather than into each internal package keeps it to one call
// site per command, and means the resolution happens once per invocation
// rather than once per component that shares a requirement.
//
// The environment is returned rather than applied to this process. Components
// run concurrently, so a toolchain written into the process environment is one
// every other component inherits — which is the whole of what "one Node
// version per repository" was.
//
// Diagnostics go to w. They are preparation notes, not gate results, and the
// caller's stdout carries the report — under --json, a document a stray line
// would make unparseable — so w is stderr or somewhere like it.
func EnsureToolchains(ctx context.Context, w io.Writer, dir string, cfg config.Config, units []toolchain.Unit) (toolchain.Envs, error) {
	return toolchain.Ensure(ctx, dir, units, toolchainOverrides(cfg), w)
}

// toolchainOverrides maps .lydite/config.yml onto the toolchain package's input.
// The mapping is explicit rather than internal/toolchain importing
// internal/config, so config stays a leaf that every other package can depend
// on without a cycle.
func toolchainOverrides(cfg config.Config) toolchain.Overrides {
	return toolchain.Overrides{
		Disabled: !cfg.Toolchain.Enabled,
		Go:       cfg.Toolchain.Go,
		Rust:     cfg.Toolchain.Rust,
		Node:     cfg.Toolchain.Node,
	}
}
