// Package rust runs Rust checks: fmt, clippy, cargo-audit, cargo-deny,
// against one component's directory. A component is the unit cargo treats as
// a whole — a workspace root, or a standalone crate — so the directory it
// declares is the directory every one of these commands runs in, and a
// workspace's member crates are covered by the one invocation cargo already
// resolves from that root.
//
// clippy's lint groups (pedantic/restriction) are configured by the target
// project's own Cargo.toml ([workspace.lints.clippy]), not by lydite — this
// package only invokes the tools with -D warnings so whatever the project
// declares is enforced as an error. Likewise, clippy/fmt's own toolchain
// version is the target repo's responsibility via its own rust-toolchain.toml
// (the standard rustup convention for pinning rustc/clippy/rustfmt together) —
// lydite doesn't second-guess that. cargo-audit and cargo-deny are different:
// they're standalone cargo subcommands with no equivalent per-repo pin, so —
// like every other scanner's toolchain (gosec/govulncheck, Biome, Semgrep) —
// lydite pins their exact versions and installs them into a version-keyed
// cache directory rather than trusting whatever's already on PATH.
package rust

import (
	"context"
	"fmt"
	"os"
	"strings"

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/executil"
)

// Check runs every Rust check in dir, with env on top of the caller's own
// environment — the component's resolved toolchain, which is what decides
// which cargo these commands find.
//
// Results are named for the tool alone. Which component they belong to is the
// caller's to say, so one labelling rule covers all three languages instead of
// three that agree until one is changed.
func Check(ctx context.Context, dir string, env executil.Env) []executil.Result {
	results := []executil.Result{
		named("cargo fmt", executil.RunEnv(ctx, dir, env.Check, "cargo", "fmt", "--check")),
		named("cargo clippy", executil.RunEnv(ctx, dir, env.Check, "cargo", "clippy", "--all-targets", "--", "-D", "warnings")),
	}

	if bin, err := ensure(ctx, env.Install, "cargo-audit", cargoAuditVersion); err != nil {
		// Detail as well as Err: report() prints Detail under a failing row
		// and nothing else, so a tool that would not install renders as a
		// bare `✗ cargo-audit` with the cause nowhere in the report.
		results = append(results, executil.Result{Name: "cargo-audit", Err: err, Detail: err.Error()})
	} else {
		results = append(results, named("cargo-audit", executil.RunEnv(ctx, dir, env.Check, bin, "audit")))
	}

	if bin, err := ensure(ctx, env.Install, "cargo-deny", cargoDenyVersion); err != nil {
		results = append(results, executil.Result{Name: "cargo-deny", Err: err, Detail: err.Error()})
	} else {
		// advisories is intentionally excluded here: cargo-audit already
		// covers RustSec CVEs, and running both would double-report them.
		results = append(results, named("cargo-deny", executil.RunEnv(ctx, dir, env.Check, bin, "deny", "check", "licenses", "bans")))
	}

	return results
}

// named returns r with Name overridden, so scan's report distinguishes each
// of the four Rust checks instead of every one showing as the literal binary
// name ("cargo").
func named(name string, r executil.Result) executil.Result {
	r.Name = name
	return r
}

// ensure installs cargo-<name> at the given version into a version-keyed
// lydite cache directory (via `cargo install --root`), so a version bump
// gets a fresh install instead of silently reusing whatever's on PATH, and
// returns the path to the installed binary. cargo-audit/cargo-deny are both
// invoked as `cargo <name> ...`, but a `--root`-installed binary is named
// plainly `cargo-<name>` and must be run directly (not via `cargo <name>`,
// which only finds cargo-* binaries already on PATH).
func ensure(ctx context.Context, env []string, name, version string) (string, error) {
	tool := cargotool.Tool{Name: name, Version: version}
	bin, err := tool.Binary()
	if err != nil {
		return "", err
	}
	if tool.Installed() {
		return bin, nil
	}
	root, err := tool.Root()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", err
	}
	argv, err := tool.InstallArgv()
	if err != nil {
		return "", err
	}
	if r := executil.RunEnv(ctx, "", env, "cargo", argv...); !r.Ok() {
		// The command's own output, not just its exit status: `exit status
		// 101` is what a failing row would otherwise carry into --json, which
		// is the document the pull-request comment renders.
		return "", fmt.Errorf("installing cargo-%s: %w\n%s", name, r.Err, strings.TrimSpace(r.Output))
	}
	return bin, nil
}
