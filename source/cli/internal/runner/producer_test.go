package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installs writes a node_modules tree holding the named packages at the given
// versions, which is what an install leaves behind and what a producer is read
// from.
func installs(t *testing.T, versions map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for pkg, version := range versions {
		dir := filepath.Join(root, "node_modules", filepath.FromSlash(pkg))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"),
			[]byte(`{"name":"`+pkg+`","version":"`+version+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A JavaScript component's producer is its runner and its coverage provider,
// read out of the tree the install produced. It is the one language whose
// measuring instrument lydite does not pin — installing one into the tree it is
// about to gate would have lydite change what the repository resolves to — so
// this is the only way to know what measured it.
func TestAJavaScriptProducerNamesTheRunnerAndTheProvider(t *testing.T) {
	root := installs(t, map[string]string{"vitest": "4.1.11", "@vitest/coverage-v8": "4.1.11"})
	got := registry[Vitest].Producer(root, "22.11.0")
	if !strings.Contains(got, "vitest 4.1.11") || !strings.Contains(got, "@vitest/coverage-v8 4.1.11") {
		t.Errorf("producer = %q, want the runner and the provider named", got)
	}
}

// The provider is looked for in order and the installed one wins, because a
// workspace carries whichever its own config selects.
func TestAJavaScriptProducerFindsTheIstanbulProvider(t *testing.T) {
	root := installs(t, map[string]string{"vitest": "4.1.11", "@vitest/coverage-istanbul": "4.1.11"})
	if got := registry[Vitest].Producer(root, "22.11.0"); !strings.Contains(got, "@vitest/coverage-istanbul 4.1.11") {
		t.Errorf("producer = %q, want the installed provider named", got)
	}
}

// Half an instrument is no answer. A producer naming the runner but not the
// provider compares equal to itself across a provider bump, which is exactly
// the comparison a producer exists to prevent — and vitest's provider is the
// half that changed what a line means.
func TestAJavaScriptProducerIsEmptyWithoutItsProvider(t *testing.T) {
	root := installs(t, map[string]string{"vitest": "4.1.11"})
	if got := registry[Vitest].Producer(root, "22.11.0"); got != "" {
		t.Errorf("producer = %q, want nothing when the provider cannot be identified", got)
	}
}

// A workspace lydite cannot introspect at all — Yarn PnP, or an install that
// never ran — has no producer rather than a wrong one. The gate then behaves as
// it did before producers existed, which is the safe answer for a repository
// whose instrument lydite cannot name.
func TestAWorkspaceWithNoInstallHasNoProducer(t *testing.T) {
	if got := registry[Vitest].Producer(t.TempDir(), "22.11.0"); got != "" {
		t.Errorf("producer = %q, want nothing when there is no node_modules", got)
	}
}

// Go's profile is the toolchain's own output, so the toolchain is the whole of
// the instrument.
func TestTheGoProducerIsTheToolchain(t *testing.T) {
	if got := registry[GoTest].Producer(t.TempDir(), "1.26.6"); got != "go 1.26.6" {
		t.Errorf("producer = %q, want the Go toolchain named", got)
	}
}

// Rust's is the pair: cargo-llvm-cov writes the lcov, and the line records
// follow the LLVM in the toolchain that built them. Naming either alone would
// compare equal across a change to the other.
func TestTheRustProducerNamesTheInstrumentationAndTheToolchain(t *testing.T) {
	got := registry[CargoNextest].Producer(t.TempDir(), "1.91.0")
	if !strings.Contains(got, "cargo-llvm-cov "+cargoLLVMCov.Version) || !strings.Contains(got, "rust 1.91.0") {
		t.Errorf("producer = %q, want both halves named", got)
	}
}

// A toolchain that would not identify itself leaves no producer, rather than
// one naming half of what measured.
func TestAnUnknownToolchainLeavesNoProducer(t *testing.T) {
	for _, name := range []Name{GoTest, CargoNextest} {
		if got := registry[name].Producer(t.TempDir(), ""); got != "" {
			t.Errorf("%s producer = %q, want nothing when the toolchain is unknown", name, got)
		}
	}
}
