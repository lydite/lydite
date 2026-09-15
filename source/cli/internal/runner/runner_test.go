package runner

import (
	"path"
	"regexp"
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
