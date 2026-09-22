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

// A component that narrows the package set its coverage is measured over is
// measured by a different instrument: the figure is a proportion of a smaller
// tree, and comparing it to one taken over the whole module reports a scope
// change as a coverage change.
func TestTheGoProducerNamesADeclaredScope(t *testing.T) {
	root := t.TempDir()
	foo := registry[GoTest].Producer(root, root, "", "1.26.6", "-coverpkg=./internal/foo/...", "./...")
	bar := registry[GoTest].Producer(root, root, "", "1.26.6", "-coverpkg=./internal/bar/...", "./...")
	if foo == bar {
		t.Errorf("both producers = %q, want a declared -coverpkg to tell them apart", foo)
	}
	if !strings.Contains(foo, "go 1.26.6") {
		t.Errorf("producer = %q, want the Go toolchain still named", foo)
	}
	whole := registry[GoTest].Producer(root, root, "", "1.26.6", "./...")
	if part := registry[GoTest].Producer(root, root, "", "1.26.6", "./internal/..."); part == whole {
		t.Errorf("both producers = %q, want the package patterns to tell them apart", whole)
	}
}

// A flag that moves no part of the measured tree leaves the producer alone. A
// producer that changed with every edit to args would report a component as
// newly measured for a timeout nobody's coverage depends on, and the first
// figure after it would have nothing to compare against.
func TestTheGoProducerIgnoresFlagsThatMoveNoDenominator(t *testing.T) {
	root := t.TempDir()
	plain := registry[GoTest].Producer(root, root, "", "1.26.6", "-race", "./...")
	timeout := registry[GoTest].Producer(root, root, "", "1.26.6", "-race", "-timeout=30s", "./...")
	if plain != timeout {
		t.Errorf("producers = %q and %q, want an unrelated flag to leave the producer alone", plain, timeout)
	}
	if got := registry[GoTest].Producer(root, root, "", "1.26.6", "-race", "./..."); got != "go 1.26.6" {
		t.Errorf("producer = %q, want the default scope to read as the toolchain alone", got)
	}
}

// A -coverpkg spelling its value in the next argument narrows the same tree as
// one spelling it after an equals sign, so the two name one scope.
func TestTheGoProducerReadsASeparatedCoverpkgValue(t *testing.T) {
	root := t.TempDir()
	inline := registry[GoTest].Producer(root, root, "", "1.26.6", "-coverpkg=./internal/...", "./...")
	separate := registry[GoTest].Producer(root, root, "", "1.26.6", "-coverpkg", "./internal/...", "./...")
	if inline != separate {
		t.Errorf("producers = %q and %q, want one scope however -coverpkg is spelled", inline, separate)
	}
}

// Everything after -args is an argument to the test binary, so a bare word
// there is not a package pattern and does not narrow anything.
func TestTheGoProducerStopsReadingPackagesAtArgs(t *testing.T) {
	root := t.TempDir()
	if got := registry[GoTest].Producer(root, root, "", "1.26.6", "./...", "-args", "fixture"); got != "go 1.26.6" {
		t.Errorf("producer = %q, want a test binary's own argument read as no scope", got)
	}
}

// Plain's build strips a declared -coverpkg from its own argv so an
// uninstrumented variant is not paid the cost of instrumentation nothing
// reads. That strip is local to the invocation it builds: Producer reads the
// declared args afresh, so a scope named for the gate still names the
// instrumented variant's producer even after Plain has been built from the
// same slice.
func TestTheGoProducerNamesAScopeAfterPlainStripsItsOwnCoverage(t *testing.T) {
	root := t.TempDir()
	args := []string{"-coverpkg=./internal/foo/...", "./..."}

	if _, ok := registry[GoTest].Build(Plain, args); !ok {
		t.Fatal("Build(Plain, ...) = false, want a Go plain invocation")
	}

	got := registry[GoTest].Producer(root, root, "", "1.26.6", args...)
	if !strings.Contains(got, "-coverpkg=./internal/foo/...") {
		t.Errorf("producer = %q, want the declared scope named after Plain was built", got)
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
	if got := registry[GoTest].Producer(root, root, "", "", "-coverpkg=./internal/...", "./..."); got != "" {
		t.Errorf("producer = %q, want a scope alone to name nothing without the toolchain that measured it", got)
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

// stubInterpreter puts a python3 ahead of any real one on PATH, answering the
// version lookup with the given lines — one per package asked about, in the
// order pythonProducer asks, and empty where the package is not installed.
// The suite must answer the same on a machine with no Python at all as on one
// whose Python happens to carry pytest.
//
// It returns the file the stub records its own argv into, NUL-separated
// because one of the arguments is a multi-line script.
func stubInterpreter(t *testing.T, lines ...string) string {
	t.Helper()
	bin := t.TempDir()
	argv := filepath.Join(bin, "argv")
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > '" + argv + "'\nprintf '%s\\n'"
	for _, line := range lines {
		script += " '" + line + "'"
	}
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(script+"\n"), 0o700); err != nil { // #nosec G306 -- a stub that has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return argv
}

// recordedArgv reads back the arguments stubInterpreter's python3 was called
// with.
func recordedArgv(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- a path this test itself made
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

// A Python component's producer is the pytest that ran the suite and the
// coverage.py that recorded its lines, read back out of the interpreter the
// invocation itself runs through. Neither is pinned, so this is the only way
// to know what measured it.
func TestAPythonProducerNamesPytestAndCoverage(t *testing.T) {
	stubInterpreter(t, "8.3.4", "7.6.1")
	dir := t.TempDir()
	if got := registry[PythonPytest].Producer(dir, dir, "", "3.13.1"); got != "pytest 8.3.4, coverage 7.6.1" {
		t.Errorf("producer = %q, want both halves named", got)
	}
}

// The lookup runs the interpreter in isolated mode. The directory it runs in
// belongs to the tree under scan, and without -I `python -c` puts that
// directory first on sys.path: importlib.metadata imports stdlib modules
// lazily and scans sys.path for *.dist-info, so the tree would choose both what
// the lookup executes — in a process holding lydite's own environment — and
// which version it reports.
func TestAPythonProducerAsksInIsolatedMode(t *testing.T) {
	argv := stubInterpreter(t, "8.3.4", "7.6.1")
	dir := t.TempDir()
	if got := registry[PythonPytest].Producer(dir, dir, "", "3.13.1"); got == "" {
		t.Fatal("producer is empty, want the stub's own answer")
	}
	got := recordedArgv(t, argv)
	if len(got) < 2 || got[0] != "-I" || got[1] != "-c" {
		t.Errorf("argv = %q, want -I ahead of -c", got)
	}
}

// Either half unidentifiable is no answer: a producer naming pytest alone
// compares equal to itself across the coverage.py bump that changed what a
// line means, which is the comparison a producer exists to prevent.
func TestAPythonProducerIsEmptyWithoutEitherHalf(t *testing.T) {
	for _, answer := range [][]string{{"8.3.4", ""}, {"", "7.6.1"}, {"", ""}} {
		stubInterpreter(t, answer...)
		dir := t.TempDir()
		if got := registry[PythonPytest].Producer(dir, dir, "", "3.13.1"); got != "" {
			t.Errorf("producer = %q for %q, want nothing when a half is unidentifiable", got, answer)
		}
	}
}

// An interpreter lydite cannot reach — no python3 on PATH, or one that answers
// with something other than a version per package — names nothing rather than
// naming what it guessed.
func TestAPythonProducerIsEmptyWithoutAnInterpreter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if got := registry[PythonPytest].Producer(dir, dir, "", "3.13.1"); got != "" {
		t.Errorf("producer = %q, want nothing when there is no interpreter", got)
	}

	stubInterpreter(t, "8.3.4")
	if got := registry[PythonPytest].Producer(dir, dir, "", "3.13.1"); got != "" {
		t.Errorf("producer = %q, want nothing when a half went unanswered", got)
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
