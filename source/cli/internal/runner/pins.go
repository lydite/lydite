package runner

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/gotool"
)

// gotestsumVersion is the wrapper a Go component's instrumented suite runs
// through, so that suite writes a JUnit report.
//
// `go test` has no report of its own: it prints for a person and exits. The
// quality-history ledger records how many tests ran and how many failed, which
// no coverage report carries and which nothing can recompute once the commit
// has been squashed away — so a Go suite either goes through something that
// writes a report or contributes no test counts at all.
//
// gotestsum rather than lydite parsing `go test -json` itself. The counts are
// the easy half of that; the hard half is the log. `-json` implies `-v`, so
// reconstructing the component's log from the event stream turns one line per
// package into two lines per test — burying a failure under thousands of
// passing tests in exactly the artifact a failing row points a reader at.
// Reconstructing the NON-verbose form instead means reimplementing the
// rendering gotestsum already does, and being subtly wrong about it. Its
// `pkgname` format is a line per package and a failures section at the end,
// which is a better log than `go test` writes, and the JUnit is the same
// format cargo-nextest and vitest write — so lydite has one reader rather than
// one per runner.
//
// A constant and not a read from the manifest beside it, for the reason
// internal/golang's are: go:embed cannot read a file inside a nested module,
// and the pin has to be a nested module or its dependency graph joins
// lydite's. internal/pins is what keeps the two agreeing.
//
// gotestsumPkg concatenates it at compile time, so the version is not stated
// twice in this file either.
const (
	gotestsumVersion = "v1.13.0"
	gotestsumPkg     = "gotest.tools/gotestsum@" + gotestsumVersion
	// gotestsumName is the program, which is also the file the install leaves
	// in its cache directory.
	gotestsumName = "gotestsum"
)

// gotestsumBinDir is where the pinned wrapper lives, for the front of PATH.
//
// Not keyed on the resolved Go toolchain, unlike the scanners internal/golang
// installs, and the difference is what the tool does with source. gosec and
// govulncheck analyse it, so one built by an older Go rejects newer source
// outright; gotestsum compiles nothing and analyses nothing — it runs `go
// test` and reads the events it prints — so a build of it is as good as any
// other, and a per-toolchain key would install the same binary once per
// component in a repository whose modules declare different Go versions.
func gotestsumBinDir() string {
	dir, err := gotool.BinDir(gotestsumName, gotestsumVersion, "")
	if err != nil {
		return ""
	}
	return dir
}

// nextestToolConfig is the config lydite layers under a repository's own so
// that cargo-nextest writes JUnit.
//
// JUnit is a profile setting in nextest's configuration file rather than a
// flag, so there is no invocation that asks for one. `--tool-config-file`
// exists for exactly this: it inserts configuration BELOW the repository's own
// in priority, so lydite turns the report on without overriding anything the
// repository said about how its tests run. That is the opposite of the stance
// internal/typescript takes with Biome's `--config-path`, and deliberately: a
// linter's rule set is lydite's verdict to fix, and a repository's test
// configuration is the repository's.
//
// It is written under the lydite cache rather than into the component, because
// the path must be absolute (nextest requires it) and because lydite must not
// leave a file in the tree it is measuring.
//
// The consequence of layering rather than overriding is stated rather than
// hidden: a repository that sets its own `[profile.default.junit] path` wins,
// and the report lands somewhere lydite does not look — so that component
// contributes no test counts, which is reported rather than guessed at.
func nextestToolConfig() (string, bool) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(cache, "lydite", "nextest-tool-config", "lydite.toml"), true
}

// nextestToolConfigBody turns the JUnit report on for the default profile, and
// says nothing else. Every other setting is the repository's.
const nextestToolConfigBody = "[profile.default.junit]\npath = \"junit.xml\"\n"

// cargoNextestManifest is the pin Dependabot watches. The version is read from
// it rather than written here so there is only one place it can be wrong — see
// docs/adr/0006-tool-pins-as-dependabot-manifests.md.
//
//go:embed cargo-nextest-pin/Cargo.toml
var cargoNextestManifest []byte

// cargoNextest is the pinned runner lydite installs for a component declaring
// cargo-nextest.
//
// It is installed rather than assumed on PATH for the reason every other tool
// lydite invokes is: whatever version a machine happens to carry decides which
// tests run and what a failure looks like. An absent one is worse than a stale
// scanner, because the component cannot run at all — `error: no such command:
// nextest` is what a runner reports when a repository declared a test command
// nobody installed.
var cargoNextest = cargotool.Tool{
	Name:     "cargo-nextest",
	Version:  cargotool.MustPinnedVersion(cargoNextestManifest, "cargo-nextest"),
	Prebuilt: nextestRelease,
}

// nextestTargets maps the platforms lydite ships for onto the release triples
// nextest publishes.
//
// macOS is one universal binary for both architectures. Linux takes the musl
// build rather than the gnu one: a musl-linked static binary runs on a glibc
// distribution as well as on Alpine, while the reverse is not true, and a
// self-hosted Alpine runner is exactly the machine that would otherwise fall
// back to a seven-minute source build.
var nextestTargets = map[string]string{
	"darwin/amd64": "universal-apple-darwin",
	"darwin/arm64": "universal-apple-darwin",
	"linux/amd64":  "x86_64-unknown-linux-musl",
	"linux/arm64":  "aarch64-unknown-linux-musl",
}

// nextestRelease locates the prebuilt archive for this machine.
//
// The tag and asset names are nextest's own scheme; a platform it does not
// publish for reports false and is built from source instead.
func nextestRelease(version string) (cargotool.Asset, bool) {
	target, ok := nextestTargets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return cargotool.Asset{}, false
	}
	base := fmt.Sprintf(
		"https://github.com/nextest-rs/nextest/releases/download/cargo-nextest-%s/cargo-nextest-%s-%s",
		version, version, target)
	return cargotool.Asset{URL: base + ".tar.gz", ChecksumURL: base + ".sha256"}, true
}

// cargoLLVMCovManifest is the pin Dependabot watches for the instrumentation
// a Rust component's coverage is measured through.
//
//go:embed cargo-llvm-cov-pin/Cargo.toml
var cargoLLVMCovManifest []byte

// cargoLLVMCov is the pinned instrumentation lydite installs before running a
// Rust component's instrumented variant.
//
// Installing it is what closes the worst failure this repository has shipped.
// `cargo llvm-cov` was assumed on PATH and never provisioned, so a runner
// without it measured nothing — and an empty baseline, once cached, is
// indistinguishable from a real one: every later pull request hits it, reports
// every component as new, and the gate enforces nothing, permanently and with
// no way to self-heal.
//
// No Prebuilt, and that is a property of the release rather than a choice.
// cargo-llvm-cov publishes archives but no checksum file beside them, and the
// digest is read out of band or not at all — lydite is about to put this
// binary on PATH and execute it. So this one is built from source, which is
// slower and says so.
var cargoLLVMCov = cargotool.Tool{
	Name:    "cargo-llvm-cov",
	Version: cargotool.MustPinnedVersion(cargoLLVMCovManifest, "cargo-llvm-cov"),
}
