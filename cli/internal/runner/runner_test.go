package runner

import (
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
		{Instrumented, "go test -coverprofile=.lydite-reports/coverage.out -coverpkg=./... -race ./..."},
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

// The JSON export carries the aggregate totals and no per-line data at all;
// the lcov export carries the per-line hits the patch gate reads. Neither is
// derivable from the other, so one run has to produce both.
func TestCargoInstrumentedExportsBothReports(t *testing.T) {
	inv := argv(t, CargoNextest, Instrumented)
	for _, want := range []string{"--json", "--lcov"} {
		if !slices.Contains(inv.Args, want) {
			t.Errorf("instrumented cargo = %v, want %s", inv.Args, want)
		}
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

func TestVitestVariants(t *testing.T) {
	for _, tc := range []struct {
		variant Variant
		want    string
	}{
		{Plain, "npx vitest run --project app"},
		{Instrumented, "npx vitest run --coverage --project app"},
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
		{Instrumented, "npx jest --coverage"},
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
