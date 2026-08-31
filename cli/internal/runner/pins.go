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
