// Package typescript runs Biome against one component's directory using a
// toolchain lydite bundles and pins itself, independent of the target
// package's own devDependencies. This avoids the failure mode where a
// package's lint script references a linter it never declares as a dependency.
//
// Biome is the only engine. It parses TypeScript with its own Rust parser and
// depends on no compiler package, which is why lydite's TypeScript linting
// carries no `typescript` pin and cannot be broken by one: the ESLint stack it
// replaced needed @typescript-eslint/parser to see .ts at all, the parser
// needed the `typescript` package as a peer, and that peer range is a moving
// ceiling a repo's own TypeScript version eventually crosses. See
// docs/adr/0008-biome-as-the-only-typescript-linter.md, and 0005 for the
// opt-in that preceded it.
//
// The pinned version lives in biome-pin/package.json, not in a Go constant, so
// Dependabot can see it and open bump PRs — a pinned security toolchain that
// nothing ever ages out is a scanner that quietly goes stale while still
// reporting [PASS].
package typescript

import (
	"context"
	"path/filepath"

	"lydite/lydite/internal/executil"
)

// Check lints dir, with env on top of the caller's own environment — the
// component's resolved Node toolchain.
//
// One invocation for the component and not one per package.json inside it:
// Biome walks the tree from where it is pointed, so a workspace's own
// packages are covered by the run at its root, and a second run inside one of
// them would report the same findings twice.
//
// The result is named for the tool alone. Which component it belongs to is
// the caller's to say.
//
// A toolchain that cannot be installed is reported as this component's failing
// result rather than returned as an error, matching how internal/rust reports
// a cargo-audit that would not install. Under the component model the
// difference is what the reader gets: an error aborts the run and discards
// every row already collected, so one component's broken install would take
// the other components' findings and Semgrep's with it.
func Check(ctx context.Context, dir string, env []string) []executil.Result {
	toolchainDir, err := ensureBiome(ctx, env)
	if err != nil {
		return []executil.Result{{Name: "biome", Err: err}}
	}
	biomeBin := filepath.Join(toolchainDir, "node_modules", ".bin", "biome")
	configPath := filepath.Join(toolchainDir, "biome.json")

	return []executil.Result{lintDirBiome(ctx, dir, env, biomeBin, configPath)}
}
