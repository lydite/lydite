package runner

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
)

// argv is the whole assertion this package can make: nothing here executes a
// foreign toolchain, so a test that shells out to cargo or npx tests the
// machine it runs on rather than the code.
func argv(t *testing.T, name Name, variant Variant, args ...string) Invocation {
	t.Helper()
	r, ok := Lookup(name)
	if !ok {
		t.Fatalf("no runner named %q", name)
	}
	inv, ok := r.Build(variant, args)
	if !ok {
		t.Fatalf("%s supplies no %s variant", name, variant)
	}
	return inv
}

func line(inv Invocation) string { return strings.Join(append([]string{inv.Name}, inv.Args...), " ") }

func TestGoTestVariants(t *testing.T) {
	for _, tc := range []struct {
		variant Variant
		want    string
	}{
		{Plain, "go test -race ./..."},
		{Instrumented, "gotestsum --format pkgname --junitfile .lydite-reports/junit.xml -- -coverprofile=.lydite-reports/coverage/coverage.out -coverpkg=./... -race ./..."},
		{BuildOnly, "go build -race ./..."},
	} {
		if got := line(argv(t, GoTest, tc.variant, "-race", "./...")); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.variant, got, tc.want)
		}
	}
}

// `go test` with no package argument tests the current directory alone, so a
// component that declared no args would report a pass having run almost
// nothing.
func TestGoTestDefaultsToTheWholePackageTree(t *testing.T) {
	if got := line(argv(t, GoTest, Plain)); got != "go test ./..." {
		t.Errorf("go-test with no args = %q", got)
	}
}

// effectiveCoverpkg is the -coverpkg `go test` acts on: flag parsing takes the
// last occurrence of a repeated flag, so the last one in the argv is the scope
// the measurement actually has. Asserting only that some -coverpkg is present
// passes just as happily with a second one appended or the ordering flipped,
// which is the failure these tests exist to catch.
func effectiveCoverpkg(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(args[i], "-coverpkg="); ok {
			return v
		}
	}
	return ""
}

// Without -coverpkg, Go instruments only the package under test, so code
// exercised solely through another package's tests reads as uncovered — a
// pull request whose new code is fully exercised from its caller fails the
// patch gate on correct, tested work.
func TestGoInstrumentedCarriesCoverpkg(t *testing.T) {
	inv := argv(t, GoTest, Instrumented)
	if got := effectiveCoverpkg(inv.Args); got != "./..." {
		t.Errorf("instrumented go-test = %v, effective -coverpkg %q, want ./...", inv.Args, got)
	}
	if inv.CoverageReport == "" {
		t.Error("the instrumented variant must name where its report lands")
	}
}

// lydite's -coverpkg=./... is a default, not a ceiling: a component whose tree
// holds generated or vendored packages narrows its coverage denominator by
// declaring its own -coverpkg, which wins because lydite's sits ahead of the
// declared args. Flip that order and the declared scope is discarded in
// silence — the component's coverage is then measured against a tree it said
// it did not mean.
func TestGoInstrumentedLetsADeclaredCoverpkgWin(t *testing.T) {
	inv := argv(t, GoTest, Instrumented, "-coverpkg=./internal/...", "-race", "./...")
	if got := effectiveCoverpkg(inv.Args); got != "./internal/..." {
		t.Errorf("instrumented go-test = %v, effective -coverpkg %q, want ./internal/...", inv.Args, got)
	}
}

// A declared coverage flag is written for the coverage gate, and only the
// instrumented variant is that gate. `go test -coverpkg=X` instruments without
// any -cover of its own, so carrying a declared one into the plain and
// build-only variants pays for instrumentation nothing reads — on the variant
// mutation runs once per mutant, thousands of times.
func TestTheUninstrumentedVariantsCarryNoCoverageFlag(t *testing.T) {
	declared := []string{
		"-coverpkg=./internal/...", "-coverprofile", "x.out", "-covermode=atomic", "-cover",
		"-race", "-timeout", "5m", "./internal/...",
	}
	for _, variant := range []Variant{Plain, BuildOnly} {
		inv := argv(t, GoTest, variant, declared...)
		for _, arg := range inv.Args {
			name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
			if strings.HasPrefix(arg, "-") && coverageFlags[name] {
				t.Errorf("%s go-test = %v, want no coverage flag", variant, inv.Args)
			}
		}
		// A coverage flag's value may be the argument behind it, and a path
		// left where `go test` expects a package is a worse argv than the one
		// the strip was for.
		if slices.Contains(inv.Args, "x.out") {
			t.Errorf("%s go-test = %v, want -coverprofile's separate value dropped with it", variant, inv.Args)
		}
		// The strip drops flags, never package patterns: a variant narrowed to
		// the component directory builds and tests almost nothing and still
		// reports a pass.
		if !slices.Contains(inv.Args, "./internal/...") {
			t.Errorf("%s go-test = %v, want the declared package pattern", variant, inv.Args)
		}
		for _, want := range []string{"-race", "-timeout", "5m"} {
			if !slices.Contains(inv.Args, want) {
				t.Errorf("%s go-test = %v, want %q kept", variant, inv.Args, want)
			}
		}
	}
	// The instrumented variant is the one the flags were declared for: it keeps
	// them, behind lydite's own, so the declared scope still wins.
	inv := argv(t, GoTest, Instrumented, declared...)
	if got := effectiveCoverpkg(inv.Args); got != "./internal/..." {
		t.Errorf("instrumented go-test = %v, effective -coverpkg %q, want ./internal/...", inv.Args, got)
	}
	if i := slices.Index(inv.Args, "-coverpkg=./..."); i < 0 || i > slices.Index(inv.Args, "-coverpkg=./internal/...") {
		t.Errorf("instrumented go-test = %v, want lydite's -coverpkg ahead of the declared one", inv.Args)
	}
}

// Instrumentation replaces the runner for Rust rather than adding a flag to
// it, which is why it cannot be a placeholder spliced into a command string.
func TestCargoInstrumentedReplacesTheRunner(t *testing.T) {
	plain := argv(t, CargoNextest, Plain, "--workspace")
	if got := line(plain); got != "cargo nextest run --workspace" {
		t.Errorf("plain = %q", got)
	}
	inst := argv(t, CargoNextest, Instrumented, "--workspace")
	if !strings.HasPrefix(line(inst), "cargo llvm-cov nextest ") {
		t.Errorf("instrumented = %q, want cargo llvm-cov", line(inst))
	}
	if !strings.HasSuffix(line(inst), "--workspace") {
		t.Errorf("instrumented = %q, want the component's args last", line(inst))
	}
}

// One export, and it is the lcov: the aggregate counts are derivable from an
// lcov's line records, and the per-line hits the patch gate reads are not
// derivable from the JSON export, which carries no line data at all.
//
// Asking for both is not merely redundant. cargo-llvm-cov names every export's
// destination with the same --output-path, so an invocation carrying two
// exports carries that flag twice and is refused at argument parsing, before
// anything executes — which a test asserting only that both flags are present
// cannot see.
func TestCargoInstrumentedExportsTheLCOVAlone(t *testing.T) {
	inv := argv(t, CargoNextest, Instrumented)
	if slices.Contains(inv.Args, "--json") {
		t.Errorf("instrumented cargo = %v, want no --json export", inv.Args)
	}
	if !slices.Contains(inv.Args, "--lcov") {
		t.Errorf("instrumented cargo = %v, want --lcov", inv.Args)
	}
	if n := slices.Index(inv.Args, "--output-path"); n < 0 {
		t.Fatalf("instrumented cargo = %v, want --output-path", inv.Args)
	}
	outputs := 0
	for _, a := range inv.Args {
		if a == "--output-path" {
			outputs++
		}
	}
	if outputs != 1 {
		t.Errorf("instrumented cargo = %v, want --output-path exactly once: cargo-llvm-cov refuses a second", inv.Args)
	}
	if inv.CoverageReport == "" || !strings.HasSuffix(inv.CoverageReport, "lcov.info") {
		t.Errorf("CoverageReport = %q, want the lcov the invocation writes", inv.CoverageReport)
	}
	if !slices.Contains(inv.Args, inv.CoverageReport) {
		t.Errorf("CoverageReport %q is not the path the invocation writes to: %v", inv.CoverageReport, inv.Args)
	}
}

// A repository that has decided to pay for instrumentation once should not
// be asked which of two runners means that.
func TestCargoLLVMCovNextestIsInstrumentedWhenPlain(t *testing.T) {
	if got, want := line(argv(t, CargoLLVMCovNextest, Plain)), line(argv(t, CargoNextest, Instrumented)); got != want {
		t.Errorf("plain = %q, want the instrumented cargo invocation %q", got, want)
	}
}

// `cargo build` alone never compiles the test targets, and a test-only
// compilation error is exactly what distinguishes an unviable mutant from a
// killed one.
func TestCargoBuildOnlyCompilesTestTargets(t *testing.T) {
	if got := line(argv(t, CargoNextest, BuildOnly)); got != "cargo build --all-targets" {
		t.Errorf("build-only = %q", got)
	}
}

// The reporter and the report directory are lydite's to name, not the
// repository's. Neither runner emits lcov by default, so a component whose own
// config says nothing would pay for the instrumentation and produce no report
// either gate can read — measured, and reported as unmeasured.
func TestJavaScriptInstrumentationNamesTheReportItWrites(t *testing.T) {
	for _, name := range []Name{Vitest, Jest} {
		inv := argv(t, name, Instrumented)
		if inv.CoverageReport == "" {
			t.Fatalf("%s's instrumented variant claims no coverage report", name)
		}
		found := false
		for _, a := range inv.Args {
			if strings.Contains(a, path.Dir(inv.CoverageReport)) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: CoverageReport %q is not a directory the invocation names: %v", name, inv.CoverageReport, inv.Args)
		}
		if !strings.HasSuffix(inv.CoverageReport, "lcov.info") {
			t.Errorf("%s: CoverageReport = %q, want the lcov both gates read", name, inv.CoverageReport)
		}
	}
}

// A runner is never pointed at the directory holding the component logs.
// Vitest empties its reports directory before a run, and for a component
// rooted at the scan root that directory holds every component's log —
// including the logs of components running concurrently beside it, whose
// failing rows then name a file that no longer exists. Reproduced against
// vitest 3.2.7 before this was written.
func TestCoverageIsWrittenBelowTheLogDirectoryAndNotAtIt(t *testing.T) {
	for _, name := range Names() {
		r, _ := Lookup(Name(name))
		inv, ok := r.Build(Instrumented, nil)
		if !ok || inv.CoverageReport == "" {
			continue
		}
		if dir := path.Dir(inv.CoverageReport); dir == ReportDir {
			t.Errorf("%s writes coverage straight into %s, where every component's log lives", name, ReportDir)
		}
		for _, a := range inv.Args {
			if strings.HasSuffix(a, "="+ReportDir) {
				t.Errorf("%s points its runner at %s itself: %q", name, ReportDir, a)
			}
		}
	}
	// The one runner known to empty what it is handed says not to.
	if inv := argv(t, Vitest, Instrumented); !slices.Contains(inv.Args, "--coverage.clean=false") {
		t.Errorf("vitest = %v, want --coverage.clean=false", inv.Args)
	}
}

func TestVitestVariants(t *testing.T) {
	for _, tc := range []struct {
		variant Variant
		want    string
	}{
		{Plain, "npx vitest run --project app"},
		// The default reporter is named beside junit deliberately:
		// --reporter=junit alone replaces the reporter set, and the
		// component's log would then hold nothing for a failing row to show.
		{Instrumented, "npx vitest run --coverage --coverage.reporter=lcovonly --coverage.reportsDirectory=" + coverageDir +
			" --coverage.clean=false --reporter=default --reporter=junit --outputFile.junit=" + junitReport + " --project app"},
		{BuildOnly, "npx tsc --noEmit"},
	} {
		if got := line(argv(t, Vitest, tc.variant, "--project", "app")); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.variant, got, tc.want)
		}
	}
}

func TestJestVariants(t *testing.T) {
	for _, tc := range []struct {
		variant Variant
		want    string
	}{
		{Plain, "npx jest"},
		{Instrumented, "npx jest --coverage --coverageReporters=lcovonly --coverageDirectory=" + coverageDir},
		{BuildOnly, "npx tsc --noEmit"},
	} {
		if got := line(argv(t, Jest, tc.variant)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.variant, got, tc.want)
		}
	}
}

// A workspace package holds no lockfile of its own, so the install a
// TypeScript component needs runs in the root above it — and a component
// installed from its own directory installs nothing at all.
func TestAWorkspacePackageIsInstalledFromItsRoot(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, root, config.Dir)
	touch(t, filepath.Join(root, "pnpm-lock.yaml"))
	dir := mkdirAll(t, root, "packages", "ui")
	cwd := stubManager(t, "pnpm")

	if err := prepareVitest(t, dir); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := readFile(t, cwd); got != root {
		t.Errorf("pnpm ran in %q, want the workspace root %q", got, root)
	}
}

// A component's own lockfile is the root, walked to or not.
func TestAComponentsOwnLockfileIsWhereItInstalls(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, root, config.Dir)
	dir := mkdirAll(t, root, "packages", "ui")
	touch(t, filepath.Join(dir, "pnpm-lock.yaml"))
	cwd := stubManager(t, "pnpm")

	if err := prepareVitest(t, dir); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := readFile(t, cwd); got != dir {
		t.Errorf("pnpm ran in %q, want the component directory %q", got, dir)
	}
}

// The walk stops at the root whose declaration named the component. A lockfile
// above that root belongs to a tree this run was never pointed at.
func TestTheInstallNeverClimbsPastTheDeclaringRoot(t *testing.T) {
	above := t.TempDir()
	touch(t, filepath.Join(above, "pnpm-lock.yaml"))
	root := mkdirAll(t, above, "repo")
	mkdirAll(t, root, config.Dir)
	dir := mkdirAll(t, root, "packages", "ui")
	cwd := stubManager(t, "pnpm")

	if err := prepareVitest(t, dir); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Errorf("an install ran from %q, above the root that declared the component", readFile(t, cwd))
	}
}

// An ancestor holding two lockfiles is no root: installing with the wrong
// manager writes a lockfile the repository does not use.
func TestAnAmbiguousRootInstallsNothing(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, root, config.Dir)
	touch(t, filepath.Join(root, "pnpm-lock.yaml"))
	touch(t, filepath.Join(root, "yarn.lock"))
	dir := mkdirAll(t, root, "packages", "ui")
	cwd := stubManager(t, "pnpm")

	if err := prepareVitest(t, dir); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Error("an install ran for a root two lockfiles name")
	}
}

// prepareVitest runs the TypeScript preparation step for a component at dir,
// with no root of its own to hand down — the mutation-worker path, which
// falls back to reading the bound off the tree.
func prepareVitest(t *testing.T, dir string) error {
	t.Helper()
	return prepareVitestFrom(t, dir, "")
}

// prepareVitestFrom is prepareVitest with an explicit root, the ordinary run's
// path: the bound is the caller's own, not read off the tree.
func prepareVitestFrom(t *testing.T, dir, root string) error {
	t.Helper()
	r, ok := Lookup(Vitest)
	if !ok {
		t.Fatal("no vitest runner")
	}
	inv, ok := r.Build(Plain, nil)
	if !ok {
		t.Fatal("vitest builds no plain variant")
	}
	return r.Prepare(context.Background(), inv, dir, root, "", executil.Env{}, io.Discard)
}

// A directory between a component and the scan root can hold a .lydite of its
// own — a vendored subtree that is itself a lydite target — and an explicit
// root is what keeps the walk from stopping there: only the caller with no
// root of its own reads the bound off the tree, and a nested .lydite is
// exactly the tree declarationRoot would stop at instead.
func TestAnExplicitRootIsNotStoppedByANestedLyditeDirectory(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, root, config.Dir)
	touch(t, filepath.Join(root, "pnpm-lock.yaml"))
	vendored := mkdirAll(t, root, "vendor", "other-project")
	mkdirAll(t, vendored, config.Dir)
	dir := mkdirAll(t, vendored, "packages", "ui")
	cwd := stubManager(t, "pnpm")

	if err := prepareVitestFrom(t, dir, root); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := readFile(t, cwd); got != root {
		t.Errorf("pnpm ran in %q, want the explicit root %q — a nested .lydite must not stop the walk short", got, root)
	}
}

// stubManager puts a program of the given name ahead of any real one on PATH,
// recording the directory it was run in at the returned path. A test that runs
// a real package manager tests the machine it runs on.
func stubManager(t *testing.T, name string) string {
	t.Helper()
	bin := t.TempDir()
	cwd := filepath.Join(bin, "cwd")
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\npwd > "+cwd+"\n"), 0o700); err != nil { // #nosec G306 -- a stub that has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cwd
}

func mkdirAll(t *testing.T, root string, parts ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// Every runner supplies all three, or mutation cannot tell an unviable
// mutant from a killed one and the score silently inflates.
func TestEveryRunnerSuppliesEveryVariant(t *testing.T) {
	for _, name := range Names() {
		r, ok := Lookup(Name(name))
		if !ok {
			t.Fatalf("Names lists %q, which Lookup does not know", name)
		}
		if r.Lang == "" {
			t.Errorf("%s declares no language", name)
		}
		for _, v := range []Variant{Plain, Instrumented, BuildOnly} {
			inv, ok := r.Build(v, nil)
			if !ok {
				t.Errorf("%s supplies no %s variant", name, v)
				continue
			}
			if inv.Name == "" {
				t.Errorf("%s's %s variant names no program", name, v)
			}
		}
	}
}

// A variant nobody defined must be refused rather than answered with an
// empty invocation, which would run the component's directory instead.
func TestUnknownVariantIsRefused(t *testing.T) {
	for _, name := range Names() {
		r, _ := Lookup(Name(name))
		if _, ok := r.Build(Variant("profile"), nil); ok {
			t.Errorf("%s answered an unknown variant", name)
		}
	}
}

// Only the instrumented variant produces coverage; a plain run that claimed
// a report path would have the gate read a file from an earlier run.
func TestOnlyTheInstrumentedVariantReportsCoverage(t *testing.T) {
	for _, name := range Names() {
		r, _ := Lookup(Name(name))
		for _, v := range []Variant{Plain, BuildOnly} {
			if name == string(CargoLLVMCovNextest) && v == Plain {
				continue
			}
			inv, _ := r.Build(v, nil)
			if inv.CoverageReport != "" {
				t.Errorf("%s's %s variant claims a coverage report at %q", name, v, inv.CoverageReport)
			}
		}
		inst, _ := r.Build(Instrumented, nil)
		if inst.CoverageReport == "" {
			t.Errorf("%s's instrumented variant names no coverage report", name)
		}
	}
}

func TestLookupRejectsAnUndeclaredRunner(t *testing.T) {
	if _, ok := Lookup("go-check"); ok {
		t.Error("Lookup accepted a runner nobody declared")
	}
}

func TestNamesIsSorted(t *testing.T) {
	got := Names()
	if !slices.IsSorted(got) {
		t.Errorf("Names = %v, want sorted so an error message reads the same every time", got)
	}
}

// The pin has to reach the invocation, or the version in the manifest is
// documentation and the runner is whatever the machine happens to carry.
func TestCargoNextestIsPinned(t *testing.T) {
	if cargoNextest.Version == "" {
		t.Fatal("no version parsed from the pin manifest")
	}
	if v := cargoNextest.Version; v[0] < '0' || v[0] > '9' {
		t.Errorf("version = %q, want a bare version with cargo's `=` operator stripped", v)
	}
	dir, err := cargoNextest.BinDir()
	if err != nil {
		t.Fatal(err)
	}
	// First, so an older cargo-nextest already on the machine cannot win once
	// the caller composes PATH.
	dirs := cargoBinDirs()
	if len(dirs) == 0 || dirs[0] != dir {
		t.Fatalf("cargoBinDirs = %v, want the pinned bin dir first", dirs)
	}
	for _, name := range []Name{CargoNextest, CargoLLVMCovNextest} {
		r, _ := Lookup(name)
		if r.Prepare == nil {
			t.Errorf("%s installs nothing, so it runs whatever is on the machine", name)
		}
		inv := argv(t, name, Plain)
		if !slices.Equal(inv.PathDirs, dirs) {
			t.Errorf("%s's plain variant PathDirs = %v, want the pinned bin dirs", name, inv.PathDirs)
		}
	}
}

// `cargo nextest` is how cargo finds a subcommand, and it is the invocation a
// reader can re-run from a failure detail — an absolute path into a
// version-keyed cache is neither.
func TestCargoInvocationStaysTypeable(t *testing.T) {
	if got := line(argv(t, CargoNextest, Plain)); got != "cargo nextest run" {
		t.Errorf("plain = %q, want the invocation a person would type", got)
	}
}

// The prebuilt release is what makes a first run tolerable: building
// cargo-nextest from source is around seven minutes and the archive is three
// seconds. A platform nextest publishes nothing for reports false and falls
// back to the source build rather than failing.
func TestNextestPrebuiltCoversEveryPlatformLyditeShipsFor(t *testing.T) {
	if _, ok := nextestTargets[runtime.GOOS+"/"+runtime.GOARCH]; !ok {
		t.Fatalf("no prebuilt target for %s/%s, which lydite ships a binary for", runtime.GOOS, runtime.GOARCH)
	}
	asset, ok := nextestRelease(cargoNextest.Version)
	if !ok {
		t.Fatal("nextestRelease reported nothing for this platform")
	}
	if !strings.Contains(asset.URL, cargoNextest.Version) {
		t.Errorf("asset URL = %q, want the pinned version in it", asset.URL)
	}
	if !strings.HasSuffix(asset.URL, ".tar.gz") || !strings.HasSuffix(asset.ChecksumURL, ".sha256") {
		t.Errorf("asset = %+v, want a tarball and its checksum", asset)
	}
}

// Linux takes the musl build: a musl-linked static binary runs on a glibc
// distribution as well as on Alpine, and the reverse is not true.
func TestNextestLinuxTargetsAreStatic(t *testing.T) {
	for platform, target := range nextestTargets {
		if strings.HasPrefix(platform, "linux/") && !strings.Contains(target, "musl") {
			t.Errorf("%s uses %q, which will not run on a musl distribution", platform, target)
		}
	}
}

// A language with a runner and no source extensions is one the orphan gate
// cannot see, so its files would go undeclared while the gate reported a
// clean pass — the declared list failing open, one level down.
//
// The implication runs this way only. A language in the table without a
// runner is the deliberate case: lydite reads a .sh as source a component has
// to claim, and runs nothing over it.
func TestEveryRunnersLangHasSourceExts(t *testing.T) {
	for _, r := range registry {
		if len(sourceExts[r.Lang]) == 0 {
			t.Errorf("runner %q is %q, which has no source extensions", r.Name, r.Lang)
		}
	}
	for _, e := range SourceExts() {
		if !strings.HasPrefix(e, ".") || e != strings.ToLower(e) {
			t.Errorf("extension %q must be lowercase and dot-prefixed", e)
		}
	}
}

// cargo-llvm-cov is installed rather than hoped for, and that closes the
// worst failure this repository has shipped: a runner without it measured
// nothing, and an empty baseline cached as real makes every later pull
// request a cache hit that gates on nothing — silently, permanently, with no
// way to self-heal.
//
// The instrumented variant is what needs it, and only it: installing it for
// every variant puts a multi-minute source build in front of a run that asked
// not to be instrumented.
func TestCargoLLVMCovIsPinnedAndOnPath(t *testing.T) {
	if cargoLLVMCov.Version == "" {
		t.Fatal("no version parsed from the pin manifest")
	}
	if v := cargoLLVMCov.Version; v[0] < '0' || v[0] > '9' {
		t.Errorf("version = %q, want a bare version with cargo's `=` operator stripped", v)
	}
	dir, err := cargoLLVMCov.BinDir()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cargoBinDirs(), dir) {
		t.Fatalf("cargoBinDirs = %v, want the pinned cargo-llvm-cov bin dir", cargoBinDirs())
	}
	// The instrumented invocation runs `cargo llvm-cov`, so the directory
	// holding it has to be on that invocation's own PATH — the registry
	// entry alone proves nothing about what the suite runs with.
	inv := argv(t, CargoNextest, Instrumented)
	if !slices.Contains(inv.PathDirs, dir) {
		t.Errorf("instrumented PathDirs = %v, want the pinned cargo-llvm-cov bin dir", inv.PathDirs)
	}
}

// The runner whose plain variant is already instrumented must still install
// the instrumentation. Keying that decision on the variant answers "not
// instrumented" for the one invocation that runs through cargo-llvm-cov, and
// the suite fails with `no such command: llvm-cov` on a machine that has
// never had it.
//
// What gets installed is read off the built command, so this asserts the
// predicate against every variant of every Rust runner rather than executing
// an install — running one here would test the machine.
func TestInstrumentationIsInstalledForWhatActuallyRunsIt(t *testing.T) {
	for _, tc := range []struct {
		name    Name
		variant Variant
		want    bool
	}{
		{CargoNextest, Plain, false},
		{CargoNextest, Instrumented, true},
		{CargoNextest, BuildOnly, false},
		{CargoLLVMCovNextest, Plain, true},
		{CargoLLVMCovNextest, Instrumented, true},
		{CargoLLVMCovNextest, BuildOnly, false},
	} {
		inv := argv(t, tc.name, tc.variant)
		if got := runsLLVMCov(inv); got != tc.want {
			t.Errorf("runsLLVMCov(%s/%s) = %v, want %v — the command is %q",
				tc.name, tc.variant, got, tc.want, line(inv))
		}
	}
}

// Runs separates the languages lydite has a runner for from the ones it only
// recognises, and it is read off the registry so the two cannot drift. A
// caller resolving a suite, a coverage report or a language's scanners asks
// this first: those all reach a Lang through a runner, and a language with
// none would resolve to nothing there while still being a file the orphan
// gate sees.
func TestRunsIsTrueForExactlyTheRunnersLanguages(t *testing.T) {
	for _, r := range registry {
		if !Runs(r.Lang) {
			t.Errorf("runner %q is %q, which Runs reports lydite does not run", r.Name, r.Lang)
		}
	}
	for _, l := range []Lang{Shell} {
		if Runs(l) {
			t.Errorf("%s has no runner in the registry, so Runs must say so", l)
		}
		if len(sourceExts[l]) == 0 {
			t.Errorf("%s has no source extensions, so the orphan gate cannot see its files", l)
		}
	}
}

// No extension may belong to two languages. LangForExt returns the first
// match from a map, so a shared extension would make the answer depend on map
// iteration order — and orphan.Unscanned groups its warning by language, so
// the same tree would report a different language run to run.
func TestNoExtensionBelongsToTwoLanguages(t *testing.T) {
	owner := map[string]Lang{}
	for lang, exts := range sourceExts {
		for _, e := range exts {
			if other, seen := owner[e]; seen {
				t.Errorf("%q belongs to both %s and %s, so LangForExt answers by map order", e, other, lang)
			}
			owner[e] = lang
		}
	}
}

// The flaky gate reads run 1's outcomes out of the report the suite wrote, so
// asking for the gate has to make the plain variant write one too — through
// the same pinned wrapper and to the same path the instrumented variant uses,
// since the ledger reads that path whichever variant ran.
func TestGoJUnitPlainIsThePlainRunThroughTheWrapper(t *testing.T) {
	inv, ok := GoJUnitPlain([]string{"-race", "./..."})
	if !ok {
		t.Fatal("GoJUnitPlain supplied no invocation")
	}
	want := "gotestsum --format pkgname --junitfile .lydite-reports/junit.xml -- -race ./..."
	if got := line(inv); got != want {
		t.Errorf("GoJUnitPlain = %q, want %q", got, want)
	}
	if inv.JUnitReport != junitReport {
		t.Errorf("GoJUnitPlain writes %q, want %q", inv.JUnitReport, junitReport)
	}
	if inv.CoverageReport != "" {
		t.Errorf("GoJUnitPlain reports coverage at %q, and a plain run measures none", inv.CoverageReport)
	}
	if empty, _ := GoJUnitPlain(nil); !slices.Contains(empty.Args, "./...") {
		t.Errorf("GoJUnitPlain with no args = %v, want the whole package tree", empty.Args)
	}
}

// Plain stays bare `go test`. Mutation runs it once per mutant, so a JUnit
// report written and discarded thousands of times is a process in the way of
// the thing being timed — which is why the gate's variant is a function of its
// own rather than a change to this one.
func TestThePlainVariantStaysBareGoTest(t *testing.T) {
	if got := line(argv(t, GoTest, Plain, "-race", "./...")); got != "go test -race ./..." {
		t.Errorf("the plain variant = %q, want bare go test", got)
	}
}

// The wrapper is installed for whatever actually runs it, and both of the
// gate's invocations do.
func TestTheGatesInvocationsRunTheWrapper(t *testing.T) {
	junitPlain, _ := GoJUnitPlain(nil)
	rerun, _ := GoRerun(nil, ".", []string{"TestOne"})
	for name, inv := range map[string]Invocation{"GoJUnitPlain": junitPlain, "GoRerun": rerun} {
		if inv.Name != gotestsumName {
			t.Errorf("%s runs %q, not the wrapper", name, inv.Name)
		}
		if !slices.Contains(inv.PathDirs, gotestsumBinDir()) {
			t.Errorf("%s PathDirs = %v, want the pinned wrapper's bin dir", name, inv.PathDirs)
		}
	}
}

// Run 2 copies run 1's argv, drops the coverage flags and replaces the package
// patterns with the one package it reruns — and ends with -run and -count=1,
// which are appended last so a declared duplicate cannot win.
func TestGoRerunCopiesTheArgvAndFiltersIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"nothing declared", nil, "-run ^(TestA|TestB)$ -count=1 ./pkg"},
		{"race is kept", []string{"-race", "./..."}, "-race -run ^(TestA|TestB)$ -count=1 ./pkg"},
		{
			"coverage flags are dropped",
			[]string{"-coverprofile=x.out", "-coverpkg=./...", "-covermode=atomic", "-cover", "-race", "./..."},
			"-race -run ^(TestA|TestB)$ -count=1 ./pkg",
		},
		{
			"a coverage flag's separate value goes with it",
			[]string{"-coverprofile", "x.out", "-race", "./..."},
			"-race -run ^(TestA|TestB)$ -count=1 ./pkg",
		},
		{
			"another flag's separate value stays with it",
			[]string{"-timeout", "5m", "./..."},
			"-timeout 5m -run ^(TestA|TestB)$ -count=1 ./pkg",
		},
		{
			"a flag after the packages is still a flag",
			[]string{"./...", "-race"},
			"-race -run ^(TestA|TestB)$ -count=1 ./pkg",
		},
		{
			"a declared -run and -count lose to the gate's",
			[]string{"-run", "TestOther", "-count=5", "./..."},
			"-run TestOther -count=5 -run ^(TestA|TestB)$ -count=1 ./pkg",
		},
		{
			// A value flag with nothing after it to carry: the bound on
			// looking one argument ahead has to refuse reading past the end
			// of the slice rather than reading its value out of bounds.
			"a value flag with no following argument is not consumed",
			[]string{"-timeout"},
			"-timeout -run ^(TestA|TestB)$ -count=1 ./pkg",
		},
		{
			// -ldflags's value routinely starts with "-" itself, as -X does
			// here. The value has to be skipped by the loop rather than
			// reprocessed as a flag of its own, or it appears twice.
			"a value that looks like a flag is not reprocessed",
			[]string{"-ldflags", "-X main.version=1.0"},
			"-ldflags -X main.version=1.0 -run ^(TestA|TestB)$ -count=1 ./pkg",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, ok := GoRerun(tc.args, "./pkg", []string{"TestA", "TestB"})
			if !ok {
				t.Fatal("GoRerun supplied no invocation")
			}
			want := "gotestsum --format pkgname --junitfile " + rerunJUnitReport + " -- " + tc.want
			if got := line(inv); got != want {
				t.Errorf("GoRerun = %q, want %q", got, want)
			}
		})
	}
}

// -count=1 is mandatory, and its absence is the failure that cannot be seen:
// run 2's argv is a cacheable subset of a run that just happened, so without
// it Go answers from run 1's own result and the gate reports determinism
// having executed nothing. It is the exact mirror of ADR 0027's rule that
// mutation must never pass it.
func TestTheRerunAlwaysCarriesCountOne(t *testing.T) {
	for _, args := range [][]string{nil, {"-race", "./..."}, {"-count=3", "./..."}} {
		inv, ok := GoRerun(args, ".", []string{"TestA"})
		if !ok {
			t.Fatalf("GoRerun over %v supplied no invocation", args)
		}
		if i := slices.Index(inv.Args, "-count=1"); i < 0 {
			t.Errorf("GoRerun over %v = %v, want -count=1", args, inv.Args)
		} else if j := slices.Index(inv.Args, "-count=3"); j > i {
			t.Errorf("GoRerun over %v puts -count=3 after -count=1, so the declared count wins", args)
		}
	}
}

// Run 2's report is its own file. The ledger records run 1's counts, and a
// rerun of five tests overwriting them would put "5 tests" in the quality
// history of a component that ran six hundred.
func TestTheRerunWritesItsOwnReport(t *testing.T) {
	inv, _ := GoRerun(nil, ".", []string{"TestA"})
	if inv.JUnitReport == junitReport {
		t.Errorf("the rerun writes %q, which is run 1's own report", inv.JUnitReport)
	}
	if inv.JUnitReport == "" {
		t.Error("the rerun writes no report, so nothing can read its outcomes")
	}
	if path.Dir(inv.JUnitReport) != ReportDir {
		t.Errorf("the rerun writes %q, want it under %q", inv.JUnitReport, ReportDir)
	}
}

// A rerun of no tests is refused. `go test -run` matching nothing prints `ok`,
// so an invocation built for an empty set reports a pass for a filter that ran
// nothing at all.
func TestARerunOfNoTestsIsRefused(t *testing.T) {
	if _, ok := GoRerun([]string{"-race"}, "./pkg", nil); ok {
		t.Error("GoRerun built an invocation for no tests")
	}
	if _, ok := GoRerun([]string{"-race"}, "", []string{"TestA"}); ok {
		t.Error("GoRerun built an invocation for no package")
	}
}

// The Rust and TypeScript suites write no report under --no-coverage either,
// so the gate has a plain-with-JUnit form of each — the report turned on, and
// none of the instrumentation.
func TestThePlainJUnitRunsCarryAReportAndNoInstrumentation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cargo, ok := CargoNextestJUnitPlain([]string{"--workspace"})
	if !ok {
		t.Fatal("CargoNextestJUnitPlain supplied no invocation")
	}
	cfg, _ := nextestToolConfig()
	want := "cargo nextest run --tool-config-file lydite:" + cfg + " --workspace"
	if got := line(cargo); got != want {
		t.Errorf("CargoNextestJUnitPlain = %q, want %q", got, want)
	}
	if cargo.JUnitReport != nextestJUnit {
		t.Errorf("CargoNextestJUnitPlain writes %q, want run 1's default-profile report", cargo.JUnitReport)
	}
	if cargo.CoverageReport != "" || slices.Contains(cargo.Args, "llvm-cov") {
		t.Errorf("CargoNextestJUnitPlain = %v, and a plain run is not instrumented", cargo.Args)
	}

	vitest, ok := VitestJUnitPlain([]string{"--project", "app"})
	if !ok {
		t.Fatal("VitestJUnitPlain supplied no invocation")
	}
	want = "npx vitest run --reporter=default --reporter=junit --outputFile.junit=" + junitReport + " --project app"
	if got := line(vitest); got != want {
		t.Errorf("VitestJUnitPlain = %q, want %q", got, want)
	}
	if vitest.JUnitReport != junitReport {
		t.Errorf("VitestJUnitPlain writes %q, want %q", vitest.JUnitReport, junitReport)
	}
	for _, a := range vitest.Args {
		if strings.HasPrefix(a, "--coverage") {
			t.Errorf("VitestJUnitPlain carries %q, and a plain run measures none", a)
		}
	}
	if vitest.CoverageReport != "" {
		t.Errorf("VitestJUnitPlain reports coverage at %q", vitest.CoverageReport)
	}
}

// Run 2 is one invocation for the whole component, filtered by OR-ed exact
// predicates and run under the profile whose report lands beside run 1's.
// --no-fail-fast, or a component with two failing new tests has the second
// reported as though it never ran.
func TestRustRerunFiltersByExactNameUnderTheRerunProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inv, ok := RustRerun([]string{"--workspace"}, []string{"shared_name", "tests::nested::doubles_deeper"})
	if !ok {
		t.Fatal("RustRerun supplied no invocation")
	}
	cfg, _ := nextestToolConfig()
	want := "cargo nextest run --tool-config-file lydite:" + cfg + " --workspace --profile rerun --no-fail-fast " +
		"-E test(=shared_name) or test(=tests::nested::doubles_deeper)"
	if got := line(inv); got != want {
		t.Errorf("RustRerun = %q, want %q", got, want)
	}
	if inv.JUnitReport != nextestRerunJUnit {
		t.Errorf("RustRerun writes %q, want %q", inv.JUnitReport, nextestRerunJUnit)
	}
	if inv.JUnitReport == nextestJUnit {
		t.Error("the rerun writes over run 1's report, whose counts the ledger records")
	}
	if slices.Contains(inv.Args, "llvm-cov") {
		t.Errorf("RustRerun = %v, want the plain runner: instrumentation the rerun pays for is read by nothing", inv.Args)
	}
}

// The filter is nextest's exact matcher, so a rerun of `doubles` cannot also
// select `doubles_deeper` — the set the gate reruns has to be the set it
// decided was new.
func TestTheNextestFilterMatchesNamesExactly(t *testing.T) {
	if got := NextestFilter([]string{"a", "b"}); got != "test(=a) or test(=b)" {
		t.Errorf("NextestFilter = %q", got)
	}
	if got := NextestFilter([]string{"only"}); got != "test(=only)" {
		t.Errorf("NextestFilter over one name = %q", got)
	}
}

// Run 2's report is its own file in every language, and both rerun paths differ
// from the path run 1 wrote.
func TestEveryRerunWritesItsOwnReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rust, _ := RustRerun(nil, []string{"a"})
	vitest, _ := VitestRerun(nil, []string{"one.test.ts"}, []string{"a"})
	for name, inv := range map[string]Invocation{"RustRerun": rust, "VitestRerun": vitest} {
		if inv.JUnitReport == "" {
			t.Errorf("%s writes no report, so nothing can read its outcomes", name)
		}
		if inv.JUnitReport == junitReport || inv.JUnitReport == nextestJUnit {
			t.Errorf("%s writes %q, which is run 1's own report", name, inv.JUnitReport)
		}
	}
}

// A rerun with nowhere to stage its tool config is refused rather than run
// blind: there is no rerun profile to select and no report to read, so the
// component is unmeasured.
func TestARustRerunWithNoToolConfigIsRefused(t *testing.T) {
	// A machine with no cache directory at all: os.UserCacheDir composes one
	// from these and errors when neither says anything.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if _, ok := nextestToolConfig(); ok {
		t.Fatal("a machine with no HOME still reports a cache directory")
	}
	if _, ok := RustRerun(nil, []string{"a"}); ok {
		t.Error("RustRerun built an invocation with nowhere for its report to land")
	}
}

// Run 2 names every file contributing a new test and carries one pattern over
// every new test's full name, with the flags it supplies itself dropped from
// what the component declared.
func TestVitestRerunNamesItsFilesAndFiltersByTitle(t *testing.T) {
	inv, ok := VitestRerun(
		[]string{"--coverage", "--coverage.reporter=lcovonly", "--reporter", "verbose", "--project", "app"},
		[]string{"libs/probe/src/one.test.ts", "libs/probe/src/two.test.ts"},
		[]string{"shared title"},
	)
	if !ok {
		t.Fatal("VitestRerun supplied no invocation")
	}
	want := "npx vitest run --reporter=default --reporter=junit --outputFile.junit=" + rerunJUnitReport +
		" --project app libs/probe/src/one.test.ts libs/probe/src/two.test.ts -t ^(shared title)$"
	if got := line(inv); got != want {
		t.Errorf("VitestRerun = %q, want %q", got, want)
	}
	if inv.CoverageReport != "" {
		t.Errorf("VitestRerun reports coverage at %q, which would overwrite the measurement the gate reads", inv.CoverageReport)
	}
}

// A declared flag whose value is the argument after it takes that value with
// it: dropping --outputFile.junit and leaving its path behind hands vitest a
// path where it expects a file filter.
func TestVitestRerunDropsWhatItSuppliesItself(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"nothing declared", nil, ""},
		{"an unrelated flag is kept", []string{"--project", "app"}, "--project app"},
		{"the coverage flags go", []string{"--coverage", "--coverage.clean=false", "--project", "app"}, "--project app"},
		{"a separate value goes with its flag", []string{"--coverage.reporter", "lcovonly", "--project", "app"}, "--project app"},
		{"a declared reporter goes", []string{"--reporter=verbose", "--project", "app"}, "--project app"},
		{"a declared report path goes with its value", []string{"--outputFile.junit", "mine.xml", "--project", "app"}, "--project app"},
		{"a declared name pattern goes with its value", []string{"-t", "something", "--project", "app"}, "--project app"},
		{"a file filter is the component's own and stays", []string{"libs/app/src/a.test.ts"}, "libs/app/src/a.test.ts"},
		{"a value flag with no following argument is not consumed", []string{"--project", "app", "-t"}, "--project app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(vitestFlags(tc.args), " "); got != tc.want {
				t.Errorf("vitestFlags(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// A title is prose, and -t is a regular expression. An alternation built
// verbatim from `matches ^a (b) [c] + d$` does not match the title it was built
// from, and vitest reports that test skipped — a deterministic test reported as
// flaky by lydite's own filter. The expected pattern is the one ADR 0041
// captured.
func TestTheTitlePatternEscapesEveryMetacharacter(t *testing.T) {
	names := []string{"matches ^a (b) [c] + d$", "shared title", "outer > inner > holds a title two describes deep"}
	want := `^(matches \^a \(b\) \[c\] \+ d\$|shared title|outer > inner > holds a title two describes deep)$`
	got := TitlePattern(names)
	if got != want {
		t.Errorf("TitlePattern = %q, want %q", got, want)
	}
	re, err := regexp.Compile(got)
	if err != nil {
		t.Fatalf("TitlePattern is not a regexp: %v", err)
	}
	for _, n := range names {
		if !re.MatchString(n) {
			t.Errorf("the pattern built from %q does not match it", n)
		}
	}
	if re.MatchString("shared title and more") {
		t.Error("the pattern is not anchored: it selects a title the gate did not decide was new")
	}
}

// A rerun of no tests is refused in every language, for the reason Go's is: an
// invocation built for an empty set reports a pass for a filter that ran
// nothing at all.
func TestARerunOfNoTestsIsRefusedInEveryLanguage(t *testing.T) {
	if _, ok := RustRerun([]string{"--workspace"}, nil); ok {
		t.Error("RustRerun built an invocation for no tests")
	}
	if _, ok := VitestRerun(nil, []string{"one.test.ts"}, nil); ok {
		t.Error("VitestRerun built an invocation for no tests")
	}
	// pytest with no node ids collects the whole suite, so this one would
	// report on every test there is rather than on none.
	if _, ok := PytestRerun([]string{"-q"}, nil); ok {
		t.Error("PytestRerun built an invocation for no tests")
	}
}

// Both profiles are declared, and they write to different files: a rerun
// landing on run 1's report would put its own count in the quality history of
// a component that ran hundreds.
func TestTheToolConfigDeclaresBothProfiles(t *testing.T) {
	for _, stanza := range []string{"[profile.default.junit]", "[profile.rerun.junit]"} {
		if !strings.Contains(nextestToolConfigBody, stanza) {
			t.Errorf("the tool config declares no %s:\n%s", stanza, nextestToolConfigBody)
		}
	}
	for _, p := range []string{`path = "junit.xml"`, `path = "junit-rerun.xml"`} {
		if !strings.Contains(nextestToolConfigBody, p) {
			t.Errorf("the tool config sets no %s:\n%s", p, nextestToolConfigBody)
		}
	}
	// The paths lydite then reads are nextest's own composition of the
	// profile's directory and the name this config gives, so the two have to
	// agree with the body.
	if path.Base(nextestJUnit) != "junit.xml" || path.Base(nextestRerunJUnit) != "junit-rerun.xml" {
		t.Errorf("the report paths %q and %q do not name what the config asks for", nextestJUnit, nextestRerunJUnit)
	}
	if path.Dir(nextestRerunJUnit) == path.Dir(nextestJUnit) {
		t.Error("both profiles write into one directory, where the rerun overwrites run 1's report")
	}
}

// The pattern is anchored at both ends, so a rerun of TestFoo cannot also
// select TestFooBar — the set the gate reruns has to be the set it decided was
// new.
func TestTheRunPatternIsAnchored(t *testing.T) {
	if got := RunPattern([]string{"TestFoo", "TestBar"}); got != "^(TestFoo|TestBar)$" {
		t.Errorf("RunPattern = %q", got)
	}
	re, err := regexp.Compile(RunPattern([]string{"TestFoo"}))
	if err != nil {
		t.Fatalf("RunPattern is not a regexp: %v", err)
	}
	if re.MatchString("TestFooBar") {
		t.Error("the pattern for TestFoo also matches TestFooBar")
	}
	if !re.MatchString("TestFoo") {
		t.Error("the pattern for TestFoo does not match TestFoo")
	}
}

func TestPytestVariants(t *testing.T) {
	for _, tc := range []struct {
		variant Variant
		want    string
	}{
		{Plain, "python3 -m pytest -q tests"},
		{Instrumented, "python3 -m pytest --cov=. --cov-report=lcov:.lydite-reports/coverage/lcov.info --junitxml=.lydite-reports/junit.xml -q tests"},
		{BuildOnly, "python3 -m pytest --collect-only -q -q tests"},
	} {
		if got := line(argv(t, PythonPytest, tc.variant, "-q", "tests")); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.variant, got, tc.want)
		}
	}
}

// The suite runs through the interpreter, not through a `pytest` on PATH: a
// bare pytest belongs to whichever environment created the script first, and
// `python3 -m` also puts the invocation's own directory on sys.path, so a
// component's package is importable without having been installed.
func TestPytestRunsThroughTheInterpreter(t *testing.T) {
	for _, v := range []Variant{Plain, Instrumented, BuildOnly} {
		inv := argv(t, PythonPytest, v)
		if inv.Name != "python3" {
			t.Errorf("%s runs %q, want the interpreter", v, inv.Name)
		}
		if len(inv.Args) < 2 || inv.Args[0] != "-m" || inv.Args[1] != "pytest" {
			t.Errorf("%s = %v, want pytest run as a module", v, inv.Args)
		}
	}
}

// A bare --cov measures nothing: coverage.py with no source named records no
// files, and the component reports unmeasured having paid for the
// instrumentation. The lcov is named too, because coverage.py's default report
// is a terminal summary neither gate can parse.
func TestPytestInstrumentationNamesItsTargetAndItsReport(t *testing.T) {
	inv := argv(t, PythonPytest, Instrumented)
	if !slices.Contains(inv.Args, "--cov=.") {
		t.Errorf("instrumented pytest = %v, want an explicit --cov target", inv.Args)
	}
	if !slices.Contains(inv.Args, "--cov-report=lcov:"+inv.CoverageReport) {
		t.Errorf("instrumented pytest = %v, want the lcov written to %q", inv.Args, inv.CoverageReport)
	}
	if !strings.HasSuffix(inv.CoverageReport, "lcov.info") {
		t.Errorf("CoverageReport = %q, want the lcov the invocation writes", inv.CoverageReport)
	}
	// --junitxml is pytest's own flag, so the report comes with the run rather
	// than through a wrapper the way Go's does.
	if inv.JUnitReport != junitReport || !slices.Contains(inv.Args, "--junitxml="+junitReport) {
		t.Errorf("instrumented pytest = %v, JUnitReport = %q, want the report asked for by name", inv.Args, inv.JUnitReport)
	}
}

// Collection is the whole of Python's build-only floor: importing every test
// module and the code it imports is the most a run can check without executing
// a test. It must not execute one — a variant that ran the suite would report
// every killed mutant as unviable.
func TestPytestBuildOnlyCollectsWithoutRunning(t *testing.T) {
	inv := argv(t, PythonPytest, BuildOnly)
	if !slices.Contains(inv.Args, "--collect-only") {
		t.Errorf("build-only pytest = %v, want --collect-only", inv.Args)
	}
}

// The flaky gate reads run 1's outcomes out of the report the suite wrote, so
// asking for the gate has to make the plain run write one — at the path the
// ledger reads whichever variant ran, and with no instrumentation, since the
// plain run measures none.
func TestPytestJUnitPlainWritesTheReportWithoutInstrumentation(t *testing.T) {
	inv, ok := PytestJUnitPlain([]string{"-q", "tests"})
	if !ok {
		t.Fatal("PytestJUnitPlain supplied no invocation")
	}
	want := "python3 -m pytest --junitxml=.lydite-reports/junit.xml -q tests"
	if got := line(inv); got != want {
		t.Errorf("PytestJUnitPlain = %q, want %q", got, want)
	}
	if inv.JUnitReport != junitReport {
		t.Errorf("PytestJUnitPlain writes %q, want %q", inv.JUnitReport, junitReport)
	}
	if inv.CoverageReport != "" {
		t.Errorf("PytestJUnitPlain reports coverage at %q, and a plain run measures none", inv.CoverageReport)
	}
}

// The rerun selects by exact node id and never by -k: a node id names the file
// as well as the test, so two tests sharing a name in different modules stay
// distinct, and it is exact, so a rerun of test_add cannot also select
// test_added the way a -k expression would.
func TestPytestRerunNamesExactNodeIds(t *testing.T) {
	ids := []string{"tests/test_a.py::test_add", "tests/test_b.py::Suite::test_add"}
	inv, ok := PytestRerun([]string{"-q"}, ids)
	if !ok {
		t.Fatal("PytestRerun supplied no invocation")
	}
	if slices.Contains(inv.Args, "-k") {
		t.Errorf("PytestRerun = %v, want no -k filter", inv.Args)
	}
	if got := inv.Args[len(inv.Args)-len(ids):]; !slices.Equal(got, ids) {
		t.Errorf("PytestRerun ends %v, want the node ids last: %v", got, ids)
	}
	if inv.JUnitReport != rerunJUnitReport {
		t.Errorf("PytestRerun writes %q, want the rerun's own report %q", inv.JUnitReport, rerunJUnitReport)
	}
	if !slices.Contains(inv.Args, "--junitxml="+rerunJUnitReport) {
		t.Errorf("PytestRerun = %v, want its own report asked for", inv.Args)
	}
}

// pytest unions its positional arguments, so a declared path left in place
// reruns the whole suite beside the tests the gate asked for — and a declared
// report path would land run 2's outcomes where the ledger reads run 1's
// counts, since pytest takes the last --junitxml.
func TestPytestRerunDropsWhatItSuppliesItself(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"nothing declared", nil, ""},
		{"an unrelated flag is kept", []string{"-q", "--strict-markers"}, "-q --strict-markers"},
		{"a declared path goes", []string{"-q", "tests", "integration"}, "-q"},
		{"the coverage flags go", []string{"--cov=src", "--cov-branch", "-q"}, "-q"},
		{"a separate coverage value goes with its flag", []string{"--cov", "src", "-q"}, "-q"},
		{"a declared report path goes with its value", []string{"--junitxml", "mine.xml", "-q"}, "-q"},
		{"a declared filter is kept with its value", []string{"-k", "not slow", "tests"}, "-k not slow"},
		{"a declared marker is kept with its value", []string{"-m", "not slow", "tests"}, "-m not slow"},
		{"a value flag with no following argument is not consumed", []string{"-q", "-k"}, "-q -k"},
		// A declared value is arbitrary text the repository chose, and one that
		// looks like a flag is passed through as the value it is: read a second
		// time as a flag of its own it would be emitted twice, and a value flag
		// spelled that way would swallow the argument behind it.
		{"a value that looks like a flag is kept once", []string{"-k", "-slow", "tests"}, "-k -slow"},
		{"a dropped flag's flag-shaped value goes with it", []string{"--cov-report", "-lcov", "-q"}, "-q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(pytestFlags(tc.args), " "); got != tc.want {
				t.Errorf("pytestFlags(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// The install is resolved from the nearest directory at or above the component
// holding exactly one recognised manifest, which is what turns a component
// nested in a repository whose requirements sit at the root into a directory an
// install is possible in.
func TestThePythonInstallIsResolvedByTheNearestManifest(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	touch(t, filepath.Join(root, "requirements.txt"))
	nested := mkdirAll(t, root, "services", "api")
	uv := mkdirAll(t, root, "services", "worker")
	touch(t, filepath.Join(uv, "uv.lock"))

	at, argv, ok := pythonInstall(nested, root)
	if !ok || at != root || !slices.Contains(argv, "-r") {
		t.Errorf("pythonInstall(%q) = %q, %v, %v, want pip installing from the root's requirements", nested, at, argv, ok)
	}
	if at, argv, ok := pythonInstall(uv, root); !ok || at != uv || argv[0] != "uv" {
		t.Errorf("pythonInstall(%q) = %q, %v, %v, want uv in the component's own directory", uv, at, argv, ok)
	}
}

// Two manifests in one directory is ambiguous and installs nothing rather than
// picking a priority order: installing through the wrong manager resolves
// versions the repository does not use. It ends the walk too — that directory
// is where this project's dependencies are declared, and a parent's manifest
// belongs to a different project.
func TestTwoPythonManifestsInstallNothing(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	touch(t, filepath.Join(root, "uv.lock"))
	dir := mkdirAll(t, root, "svc")
	touch(t, filepath.Join(dir, "poetry.lock"))
	touch(t, filepath.Join(dir, "requirements.txt"))

	// Nothing to install is nothing to run and nowhere to run it: a directory
	// named beside a false answer is one a caller reading past it would install
	// in through whichever manager it guessed.
	if at, argv, ok := pythonInstall(dir, root); ok || at != "" || argv != nil {
		t.Errorf("pythonInstall = %q, %v, %v, want nothing for an ambiguous directory", at, argv, ok)
	}
}

// Nothing above the bound is installed from: a tree lydite was not pointed at
// is another project's, and its manifest is not this component's to install.
func TestThePythonInstallWalkStopsAtTheRoot(t *testing.T) {
	outer := t.TempDir()
	touch(t, filepath.Join(outer, "requirements.txt"))
	root := mkdirAll(t, outer, "repo")
	dir := mkdirAll(t, root, "svc")

	if at, argv, ok := pythonInstall(dir, root); ok || at != "" || argv != nil {
		t.Errorf("pythonInstall = %q, %v, %v, want nothing above the bound", at, argv, ok)
	}
}

// The root the caller hands down is the bound, and preparing a component never
// derives one of its own instead: a directory between the component and the
// scan root can hold a .lydite — a vendored subtree that is itself a lydite
// target — so a re-derived bound reaches for a manifest belonging to a
// different tree.
func TestThePythonInstallHonoursTheRootItIsGiven(t *testing.T) {
	outer := t.TempDir()
	mkdirAll(t, outer, config.Dir)
	touch(t, filepath.Join(outer, "uv.lock"))
	root := mkdirAll(t, outer, "repo")
	dir := mkdirAll(t, root, "svc")
	cwd := stubManager(t, "uv")

	r, ok := Lookup(PythonPytest)
	if !ok {
		t.Fatal("no python-pytest runner")
	}
	inv, _ := r.Build(Plain, nil)
	if err := r.Prepare(context.Background(), inv, dir, root, "", executil.Env{}, io.Discard); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Errorf("uv ran in %q, and the lockfile above %q is another tree's", readFile(t, cwd), root)
	}
}

// A component's dependencies are installed in the directory that declares them,
// with the repository's own environment: this is the repository's package
// manager reading the repository's lockfile, unlike a pinned tool of lydite's.
func TestThePythonInstallRunsInTheManifestsDirectory(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, root, config.Dir)
	touch(t, filepath.Join(root, "uv.lock"))
	dir := mkdirAll(t, root, "svc")
	cwd := stubManager(t, "uv")

	r, ok := Lookup(PythonPytest)
	if !ok {
		t.Fatal("no python-pytest runner")
	}
	inv, _ := r.Build(Plain, nil)
	if err := r.Prepare(context.Background(), inv, dir, "", "", executil.Env{}, io.Discard); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := readFile(t, cwd); got != root {
		t.Errorf("uv ran in %q, want the directory holding the lockfile %q", got, root)
	}
}

// A tree with no manifest lydite recognises has nothing that can be installed
// without guessing, and a pytest run against an environment somebody else
// provisioned is an ordinary way to run a Python suite — so preparing it is a
// no-op rather than a failure. A component whose dependencies are genuinely
// missing fails its own collection, naming them.
func TestAPythonComponentWithNoManifestPreparesWithoutFailing(t *testing.T) {
	root := mkdirAll(t, t.TempDir(), "repo")
	mkdirAll(t, root, config.Dir)
	dir := mkdirAll(t, root, "svc")

	r, _ := Lookup(PythonPytest)
	inv, _ := r.Build(Plain, nil)
	if err := r.Prepare(context.Background(), inv, dir, root, "", executil.Env{}, io.Discard); err != nil {
		t.Errorf("Prepare: %v, want nothing to do", err)
	}
}
