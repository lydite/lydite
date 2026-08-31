package cargotool

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPinnedVersionStripsCargosExactOperator(t *testing.T) {
	v, err := PinnedVersion([]byte("[dependencies]\ncargo-nextest = \"=0.9.1\"\n"), "cargo-nextest")
	if err != nil {
		t.Fatalf("PinnedVersion: %v", err)
	}
	if v != "0.9.1" {
		t.Errorf("PinnedVersion = %q, want the bare version", v)
	}
}

func TestPinnedVersionMissingCrateIsAnError(t *testing.T) {
	if _, err := PinnedVersion([]byte("[dependencies]\ncargo-audit = \"=1.2.3\"\n"), "cargo-nope"); err == nil {
		t.Error("PinnedVersion accepted a crate the manifest does not pin")
	}
}

// A pin manifest also carries `version = "0.0.0"` and `name = "..."` in its
// [package] table, and a section-blind parser answers a lookup with that
// unrelated metadata.
func TestPinnedVersionIgnoresPackageMetadata(t *testing.T) {
	manifest := []byte("[package]\nname = \"pin\"\nversion = \"0.0.0\"\n\n[dependencies]\ncargo-audit = \"=1.2.3\"\n")
	if v, err := PinnedVersion(manifest, "version"); err == nil {
		t.Errorf("PinnedVersion matched the [package] version field, returning %q", v)
	}
}

// An inline comment left attached survives into `cargo install --version` as a
// corrupt value, where the parser promises a loud failure instead. This repo
// appends `# nosemgrep:` comments to config lines elsewhere, so it is not a
// hypothetical shape.
func TestPinnedVersionStripsInlineComment(t *testing.T) {
	v, err := PinnedVersion([]byte("[dependencies]\ncargo-audit = \"=1.2.3\" # pinned deliberately\n"), "cargo-audit")
	if err != nil {
		t.Fatalf("PinnedVersion: %v", err)
	}
	if v != "1.2.3" {
		t.Errorf("PinnedVersion = %q, want %q", v, "1.2.3")
	}
}

// An unparseable manifest must fail loudly rather than yield "", which becomes
// `cargo install --version ”` — a confusing failure far from its cause, or an
// install of whatever is latest.
func TestPinnedVersionRejectsAValueItCannotRead(t *testing.T) {
	for _, manifest := range []string{
		"[dependencies]\ncargo-audit = 1.2.3\n",
		"[dependencies]\ncargo-audit = \"=1.2.3\n",
		"[dependencies]\ncargo-audit = \"=\"\n",
	} {
		if v, err := PinnedVersion([]byte(manifest), "cargo-audit"); err == nil {
			t.Errorf("PinnedVersion(%q) = %q, want an error", manifest, v)
		}
	}
}

// Keyed by version, so two lydite releases pinning different ones do not
// overwrite each other and a downgrade does not silently keep the newer binary.
func TestRootIsKeyedByNameAndVersion(t *testing.T) {
	a, err := Tool{Name: "cargo-nextest", Version: "0.9.1"}.Root()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Tool{Name: "cargo-nextest", Version: "0.9.2"}.Root()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two versions share a cache directory: %q", a)
	}
	if filepath.Base(a) != "cargo-nextest-0.9.1" {
		t.Errorf("Root = %q, want it keyed by name and version", a)
	}
}

// --locked, or cargo re-resolves and the tool lydite installs is built from a
// different graph than its authors tested — a pin in name only.
func TestInstallArgvIsLocked(t *testing.T) {
	tool := Tool{Name: "cargo-nextest", Version: "0.9.1"}
	argv, err := tool.InstallArgv()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, "--locked") {
		t.Errorf("InstallArgv = %v, want --locked", argv)
	}
	if !slices.Contains(argv, "0.9.1") {
		t.Errorf("InstallArgv = %v, want the pinned version", argv)
	}
	if argv[len(argv)-1] != "cargo-nextest" {
		t.Errorf("InstallArgv = %v, want the crate last", argv)
	}
	if !strings.HasPrefix(strings.Join(argv, " "), "install ") {
		t.Errorf("InstallArgv = %v, want a cargo install invocation", argv)
	}
}

func TestNotInstalledWhenTheBinaryIsAbsent(t *testing.T) {
	if (Tool{Name: "cargo-does-not-exist", Version: "9.9.9"}).Installed() {
		t.Error("Installed reported a tool that was never installed")
	}
}
