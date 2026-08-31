package rust

import (
	"testing"

	"lydite/lydite/internal/cargotool"
)

// TestPinnedVersionsParse guards the manifest→runtime path itself. Dependabot
// edits these manifests, so a bump that changed a line's shape (an inline table
// for a feature flag, a trailing comment) must fail here rather than silently
// yielding a corrupt value that reaches `cargo install --version`.
func TestPinnedVersionsParse(t *testing.T) {
	for _, tc := range []struct {
		manifest []byte
		crate    string
	}{
		{cargoAuditManifest, "cargo-audit"},
		{cargoDenyManifest, "cargo-deny"},
	} {
		v, err := cargotool.PinnedVersion(tc.manifest, tc.crate)
		if err != nil {
			t.Errorf("PinnedVersion(%q): %v", tc.crate, err)
			continue
		}
		if v == "" || v[0] < '0' || v[0] > '9' {
			t.Errorf("PinnedVersion(%q) = %q; want a bare version with cargo's `=` operator stripped", tc.crate, v)
		}
	}
}

// TestPinManifestsAreSeparate guards the reason there are two manifests at all:
// cargo-audit and cargo-deny cannot resolve in one dependency graph, so a
// well-meaning consolidation would produce a manifest cargo errors on and
// Dependabot silently skips.
func TestPinManifestsAreSeparate(t *testing.T) {
	if _, err := cargotool.PinnedVersion(cargoAuditManifest, "cargo-deny"); err == nil {
		t.Error("cargo-deny is declared in cargo-audit-pin; the two must not share a resolution graph")
	}
	if _, err := cargotool.PinnedVersion(cargoDenyManifest, "cargo-audit"); err == nil {
		t.Error("cargo-audit is declared in cargo-deny-pin; the two must not share a resolution graph")
	}
}
