package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/config"
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
	got := registry[Vitest].Producer(root, root, "", "22.11.0")
	if !strings.Contains(got, "vitest 4.1.11") || !strings.Contains(got, "@vitest/coverage-v8 4.1.11") {
		t.Errorf("producer = %q, want the runner and the provider named", got)
	}
}

// The provider is looked for in order and the installed one wins, because a
// workspace carries whichever its own config selects.
func TestAJavaScriptProducerFindsTheIstanbulProvider(t *testing.T) {
	root := installs(t, map[string]string{"vitest": "4.1.11", "@vitest/coverage-istanbul": "4.1.11"})
	if got := registry[Vitest].Producer(root, root, "", "22.11.0"); !strings.Contains(got, "@vitest/coverage-istanbul 4.1.11") {
		t.Errorf("producer = %q, want the installed provider named", got)
	}
}

// Half an instrument is no answer. A producer naming the runner but not the
// provider compares equal to itself across a provider bump, which is exactly
// the comparison a producer exists to prevent — and vitest's provider is the
// half that changed what a line means.
func TestAJavaScriptProducerIsEmptyWithoutItsProvider(t *testing.T) {
	root := installs(t, map[string]string{"vitest": "4.1.11"})
	if got := registry[Vitest].Producer(root, root, "", "22.11.0"); got != "" {
		t.Errorf("producer = %q, want nothing when the provider cannot be identified", got)
	}
}

// A workspace lydite cannot introspect at all — Yarn PnP, or an install that
// never ran — has no producer rather than a wrong one. Two such measurements
// compare, which is the safe answer for a repository whose instrument lydite
// cannot name: the alternative never gates it again.
func TestAWorkspaceWithNoInstallHasNoProducer(t *testing.T) {
	root := t.TempDir()
	if got := registry[Vitest].Producer(root, root, "", "22.11.0"); got != "" {
		t.Errorf("producer = %q, want nothing when there is no node_modules", got)
	}
}

// Go's profile is the toolchain's own output, so the toolchain is the whole of
// the instrument.
func TestTheGoProducerIsTheToolchain(t *testing.T) {
	root := t.TempDir()
	if got := registry[GoTest].Producer(root, root, "", "1.26.6"); got != "go 1.26.6" {
		t.Errorf("producer = %q, want the Go toolchain named", got)
	}
}

// Rust's is the pair: cargo-llvm-cov writes the lcov, and the line records
// follow the LLVM in the toolchain that built them. Naming either alone would
// compare equal across a change to the other.
func TestTheRustProducerNamesTheInstrumentationAndTheToolchain(t *testing.T) {
	root := t.TempDir()
	got := registry[CargoNextest].Producer(root, root, "", "1.91.0")
	if !strings.Contains(got, "cargo-llvm-cov "+cargoLLVMCov.Version) || !strings.Contains(got, "rust 1.91.0") {
		t.Errorf("producer = %q, want both halves named", got)
	}
}

// A toolchain that would not identify itself leaves no producer, rather than
// one naming half of what measured.
func TestAnUnknownToolchainLeavesNoProducer(t *testing.T) {
	root := t.TempDir()
	for _, name := range []Name{GoTest, CargoNextest} {
		if got := registry[name].Producer(root, root, "", ""); got != "" {
			t.Errorf("%s producer = %q, want nothing when the toolchain is unknown", name, got)
		}
	}
}

// Jest instruments through the babel plugin it bundles, so its own version is
// the whole of the instrument and there is no provider to name beside it.
func TestTheJestProducerIsTheRunnerAlone(t *testing.T) {
	root := installs(t, map[string]string{"jest": "30.2.0"})
	if got := registry[Jest].Producer(root, root, "", "22.11.0"); got != "jest 30.2.0" {
		t.Errorf("producer = %q, want the runner alone", got)
	}
	empty := t.TempDir()
	if got := registry[Jest].Producer(empty, empty, "", "22.11.0"); got != "" {
		t.Errorf("producer = %q, want nothing when jest is not installed", got)
	}
}

// The instrumented variant of cargo-llvm-cov-nextest measures through the same
// pair as cargo-nextest's, so it names the same producer. A runner whose plain
// variant is already instrumented must not answer differently from one whose
// instrumented variant is.
func TestBothRustRunnersNameTheSameProducer(t *testing.T) {
	dir, lang := t.TempDir(), "1.91.0"
	if a, b := registry[CargoNextest].Producer(dir, dir, "", lang), registry[CargoLLVMCovNextest].Producer(dir, dir, "", lang); a != b {
		t.Errorf("cargo-nextest = %q, cargo-llvm-cov-nextest = %q, want one answer", a, b)
	}
}

// A component nested in a workspace whose install hoisted the runner above it
// still has a producer: the lookup follows the same walk Install resolved,
// not the component's own empty node_modules.
func TestAProducerFollowsTheWorkspaceRootInstallResolved(t *testing.T) {
	scanRoot := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, scanRoot, config.Dir)
	touch(t, filepath.Join(scanRoot, "pnpm-lock.yaml"))
	dir := mkdirAll(t, scanRoot, "packages", "ui")
	writePackage(t, scanRoot, "vitest", "4.1.11")
	writePackage(t, scanRoot, "@vitest/coverage-v8", "4.1.11")

	got := registry[Vitest].Producer(dir, scanRoot, "", "22.11.0")
	if !strings.Contains(got, "vitest 4.1.11") || !strings.Contains(got, "@vitest/coverage-v8 4.1.11") {
		t.Errorf("producer = %q, want the runner and provider named from the hoisted root", got)
	}
}

// An override runs in the component's own directory regardless of any
// lockfile above it, so its producer has to be read from there too — a
// workspace root above a component with no runner of its own must not be
// mistaken for where an override installed.
func TestAnOverrideProducerReadsTheComponentDirectoryNotTheWorkspaceRoot(t *testing.T) {
	scanRoot := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, scanRoot, config.Dir)
	touch(t, filepath.Join(scanRoot, "pnpm-lock.yaml"))
	dir := mkdirAll(t, scanRoot, "packages", "ui")
	writePackage(t, dir, "vitest", "4.1.11")
	writePackage(t, dir, "@vitest/coverage-v8", "4.1.11")

	got := registry[Vitest].Producer(dir, scanRoot, "npm install", "22.11.0")
	if !strings.Contains(got, "vitest 4.1.11") || !strings.Contains(got, "@vitest/coverage-v8 4.1.11") {
		t.Errorf("producer = %q, want the runner and provider named from the component's own directory", got)
	}
}

// writePackage stages one package.json under root/node_modules, the way
// installs does but for a single named version — used where the fixture also
// needs a lockfile beside it, which installs' own root has no room for.
func writePackage(t *testing.T, root, pkg, version string) {
	t.Helper()
	dir := filepath.Join(root, "node_modules", filepath.FromSlash(pkg))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"`+pkg+`","version":"`+version+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}
