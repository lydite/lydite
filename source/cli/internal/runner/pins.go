package runner

import (
	_ "embed"
	"fmt"
	"runtime"

	"lydite/lydite/internal/cargotool"
)

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
