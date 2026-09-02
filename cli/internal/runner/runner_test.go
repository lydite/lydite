package runner

import (
	"path"
	"runtime"
	"slices"
	"strings"
	"testing"
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
		{Instrumented, "go test -coverprofile=.lydite-reports/coverage/coverage.out -coverpkg=./... -race ./..."},
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

// Without -coverpkg, Go instruments only the package under test, so code
// exercised solely through another package's tests reads as uncovered — a
// pull request whose new code is fully exercised from its caller fails the
// patch gate on correct, tested work.
func TestGoInstrumentedCarriesCoverpkg(t *testing.T) {
	inv := argv(t, GoTest, Instrumented)
	if !slices.Contains(inv.Args, "-coverpkg=./...") {
		t.Errorf("instrumented go-test = %v, want -coverpkg=./...", inv.Args)
	}
	if inv.CoverageReport == "" {
		t.Error("the instrumented variant must name where its report lands")
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
		{Instrumented, "npx vitest run --coverage --coverage.reporter=lcovonly --coverage.reportsDirectory=" + coverageDir + " --coverage.clean=false --project app"},
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
func TestEveryLangHasSourceExts(t *testing.T) {
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
