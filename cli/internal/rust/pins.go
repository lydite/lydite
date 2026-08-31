package rust

import (
	_ "embed"

	"lydite/lydite/internal/cargotool"
)

// One manifest per tool: cargo-audit and cargo-deny cannot resolve in a shared
// dependency graph (cargo-deny's krates pins petgraph =0.8.1, cargo-audit's
// cargo-lock pulls 0.8.2), and a manifest that cannot resolve is one Dependabot
// silently errors on — the exact "pin nothing ages out" failure this arrangement
// exists to prevent.
var (
	//go:embed cargo-audit-pin/Cargo.toml
	cargoAuditManifest []byte
	//go:embed cargo-deny-pin/Cargo.toml
	cargoDenyManifest []byte
)

// cargoAuditVersion and cargoDenyVersion are read from cargo-pin/Cargo.toml
// rather than written here, so that Dependabot has a manifest it understands
// and there is only one place a version can be wrong. A pinned security tool
// that nothing ever ages out is a scanner that quietly goes stale while still
// reporting [PASS].
var (
	cargoAuditVersion = cargotool.MustPinnedVersion(cargoAuditManifest, "cargo-audit")
	cargoDenyVersion  = cargotool.MustPinnedVersion(cargoDenyManifest, "cargo-deny")
)

// The parsing and the install both live in internal/cargotool, because the
// component test runners pin cargo subcommands the same way and a second copy
// of either would drift from this one.
