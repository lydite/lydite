package runner

import (
	_ "embed"

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
	Name:    "cargo-nextest",
	Version: cargotool.MustPinnedVersion(cargoNextestManifest, "cargo-nextest"),
}
