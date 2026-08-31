// Package runner turns a component's declared runner into the three
// invocations lydite needs of the same suite.
//
// lydite orchestrates; it does not know how to run anyone's tests and must
// not learn. A runner names a test command lydite invokes — `go test`,
// `cargo nextest`, `vitest` — and this package holds the small amount lydite
// has to know about each: which language it implies, and how to ask that
// tool for a variant of the same run.
//
// The three variants exist for reasons that do not overlap:
//
//   - Plain is the fast path. Mutation runs the suite once per mutant, so
//     anything the coverage gate needs and a mutant does not is pure cost
//     multiplied by the number of mutants.
//   - Instrumented is the coverage gate, and mutation's baseline.
//   - BuildOnly tells an unviable mutant from a killed one. Both exit
//     non-zero, so without a compile step first the mutation score silently
//     inflates: every mutant that does not compile counts as killed.
//
// Deriving all three from one declaration is what stops them disagreeing
// about which tests they run. Instrumentation is not a flag that can be
// spliced into an arbitrary command: Go appends -coverprofile, and Rust
// replaces the runner outright with cargo llvm-cov. A single command string
// carrying a placeholder covers the first case and has nowhere to put the
// second, which is why a component either names a runner or opts out of the
// derived variants entirely.
//
// Nothing here executes anything. It builds argv, and its tests assert argv,
// matching internal/rust and internal/typescript: a unit test that shells out
// to a foreign toolchain tests the machine it runs on.
package runner

import (
	"path"
	"sort"

	"lydite/lydite/internal/nodedeps"
)

// Name identifies a runner in a component declaration.
type Name string

// The runners lydite ships. A component names one of these or supplies its
// own command.
const (
	// GoTest is `go test`.
	GoTest Name = "go-test"
	// CargoNextest is `cargo nextest run`.
	CargoNextest Name = "cargo-nextest"
	// CargoLLVMCovNextest is `cargo llvm-cov nextest`, for a component that
	// wants the instrumented form as its plain one too.
	CargoLLVMCovNextest Name = "cargo-llvm-cov-nextest"
	// Vitest is `vitest run`.
	Vitest Name = "vitest"
	// Jest is `jest`.
	Jest Name = "jest"
)

// Lang is the language a runner implies. It is derived, never declared: a
// component naming cargo-nextest can only be Rust, and a second statement of
// that could only disagree with the first.
type Lang string

// The languages lydite runs.
const (
	// Go is the Go toolchain.
	Go Lang = "go"
	// Rust is the Cargo toolchain.
	Rust Lang = "rust"
	// TypeScript is the Node toolchain.
	TypeScript Lang = "typescript"
)

// Variant is which of the three forms of a suite is wanted.
type Variant string

const (
	// Plain runs the suite with nothing added.
	Plain Variant = "plain"
	// Instrumented runs it under coverage instrumentation.
	Instrumented Variant = "instrumented"
	// BuildOnly compiles without running anything.
	BuildOnly Variant = "build-only"
)

// Invocation is one command to run, in a component's directory.
type Invocation struct {
	// Name is the program, resolved on PATH by the caller.
	Name string
	// Args is its argv, excluding Name.
	Args []string
	// CoverageReport is where the instrumented variant writes its report,
	// relative to the component directory. Empty for every other variant,
	// and for a runner whose report path is a fixed convention the reader
	// already knows.
	CoverageReport string
	// JUnitReport is where the run writes a JUnit XML file, relative to the
	// component directory, or empty when this runner emits none. The
	// quality-history ledger records test counts, which is a number no
	// coverage report carries.
	JUnitReport string
	// Optional marks a preparation step whose failure is not the run
	// failing. A suite invocation is never optional.
	Optional bool
}

// Runner is what lydite knows about one test command.
type Runner struct {
	// Name is the value a component declares.
	Name Name
	// Lang is the language this runner implies.
	Lang Lang
	// Build constructs one variant's invocation, with args from the
	// component declaration placed ahead of anything the variant adds.
	Build func(variant Variant, args []string) (Invocation, bool)
	// Prepare is what has to run in the component's directory before any
	// variant will work, or nil when the toolchain needs nothing.
	//
	// Only the JavaScript runners have one. A fresh checkout has no
	// node_modules and every import fails before a single test is collected,
	// while `go test` and `cargo` fetch what a build needs on the way past.
	// It lives on the runner rather than in the command so the command layer
	// carries no per-language branch — the thing this registry exists to
	// remove.
	Prepare func(dir, override string) []Invocation
}

// registry is the whole set, keyed by declared name.
var registry = map[Name]Runner{
	GoTest:              {Name: GoTest, Lang: Go, Build: buildGoTest},
	CargoNextest:        {Name: CargoNextest, Lang: Rust, Build: buildCargoNextest},
	CargoLLVMCovNextest: {Name: CargoLLVMCovNextest, Lang: Rust, Build: buildCargoLLVMCovNextest},
	Vitest:              {Name: Vitest, Lang: TypeScript, Build: buildVitest, Prepare: installNodeDeps},
	Jest:                {Name: Jest, Lang: TypeScript, Build: buildJest, Prepare: installNodeDeps},
}

// Lookup returns the runner a component declared.
func Lookup(name Name) (Runner, bool) {
	r, ok := registry[name]
	return r, ok
}

// Names returns every declarable runner name, sorted, for error messages
// that tell the reader what they could have written instead.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, string(n))
	}
	sort.Strings(out)
	return out
}

// ReportDir is where lydite puts the reports it asks a runner to write,
// relative to the component directory. One directory so a component's
// artefacts are collectable without knowing which runner produced them.
const ReportDir = ".lydite-reports"

func report(name string) string { return path.Join(ReportDir, name) }

// goTestArgs defaults to the module's whole package tree, because a `go
// test` with no package argument tests the current directory alone — a
// component that declared no args would report a pass having run almost
// nothing.
func goTestArgs(args []string) []string {
	if len(args) == 0 {
		return []string{"./..."}
	}
	return args
}

// buildGoTest derives Go's three variants.
//
// The instrumented form carries -coverpkg=./... as well as -coverprofile.
// Without it Go instruments only the package under test, so code exercised
// solely through another package's tests is recorded as uncovered: layered
// code is penalised exactly in proportion to how well it is layered, and a
// pull request whose new code is fully exercised from its caller fails the
// patch gate. The build-only variant is `go build`, not `go vet` or a
// compile-only test flag, because what it has to answer is whether the
// package compiles at all.
func buildGoTest(variant Variant, args []string) (Invocation, bool) {
	pkgs := goTestArgs(args)
	switch variant {
	case Plain:
		return Invocation{Name: "go", Args: append([]string{"test"}, pkgs...)}, true
	case Instrumented:
		profile := report("coverage.out")
		return Invocation{
			Name:           "go",
			Args:           append([]string{"test", "-coverprofile=" + profile, "-coverpkg=./..."}, pkgs...),
			CoverageReport: profile,
		}, true
	case BuildOnly:
		return Invocation{Name: "go", Args: append([]string{"build"}, pkgs...)}, true
	default:
		return Invocation{}, false
	}
}

// nextestJUnit is where cargo-nextest writes JUnit, which is a profile
// setting in the repository's own .config/nextest.toml rather than a flag.
// Naming the path here would be lydite claiming a file it does not write.
const nextestJUnit = "target/nextest/default/junit.xml"

func buildCargoNextest(variant Variant, args []string) (Invocation, bool) {
	switch variant {
	case Plain:
		return Invocation{
			Name:        "cargo",
			Args:        append([]string{"nextest", "run"}, args...),
			JUnitReport: nextestJUnit,
		}, true
	case Instrumented:
		return llvmCovNextest(args), true
	case BuildOnly:
		// --all-targets, because a test-only compilation error is exactly
		// what distinguishes an unviable mutant from a killed one, and
		// `cargo build` alone never compiles the test targets.
		return Invocation{Name: "cargo", Args: []string{"build", "--all-targets"}}, true
	default:
		return Invocation{}, false
	}
}

// buildCargoLLVMCovNextest is for a component whose plain run is already
// instrumented. Its three variants are the same shape as cargo-nextest's,
// with the plain one replaced: a repository that has decided to pay for
// instrumentation once should not be asked which of two runners means that.
func buildCargoLLVMCovNextest(variant Variant, args []string) (Invocation, bool) {
	if variant == Plain {
		return llvmCovNextest(args), true
	}
	return buildCargoNextest(variant, args)
}

// llvmCovNextest replaces the runner rather than adding a flag to it, which
// is why instrumentation cannot be expressed as a placeholder spliced into
// an arbitrary command.
//
// Both exports come from one run: --json carries the aggregate totals and no
// per-line data at all, and --lcov carries the per-line hits the patch gate
// reads. Asking for one and deriving the other is not possible in either
// direction.
func llvmCovNextest(args []string) Invocation {
	json := report("llvm-cov.json")
	return Invocation{
		Name: "cargo",
		Args: append([]string{
			"llvm-cov", "nextest",
			"--json", "--output-path", json,
			"--lcov", "--output-path", report("lcov.info"),
		}, args...),
		CoverageReport: json,
		JUnitReport:    nextestJUnit,
	}
}

// installNodeDeps is the install internal/nodedeps resolves from the
// component root's lockfile, or from the typescript.install override.
//
// An empty result is not a failure: a root no single lockfile identifies has
// nothing lydite can install without guessing, and guessing writes a lockfile
// the repository does not use.
func installNodeDeps(dir, override string) []Invocation {
	var out []Invocation
	for _, cmd := range nodedeps.Commands(dir, override) {
		out = append(out, Invocation{Name: cmd.Argv[0], Args: cmd.Argv[1:], Optional: cmd.Optional})
	}
	return out
}

// buildVitest runs through the package manager's own binary directory rather
// than a global vitest, so the version the repository pinned is the version
// that runs.
func buildVitest(variant Variant, args []string) (Invocation, bool) {
	switch variant {
	case Plain:
		return Invocation{Name: "npx", Args: append([]string{"vitest", "run"}, args...), JUnitReport: report("junit.xml")}, true
	case Instrumented:
		return Invocation{
			Name:           "npx",
			Args:           append([]string{"vitest", "run", "--coverage"}, args...),
			CoverageReport: "coverage/lcov.info",
			JUnitReport:    report("junit.xml"),
		}, true
	case BuildOnly:
		// tsc, because there is no compile step in a JavaScript test run to
		// separate an unviable mutant from a killed one — a syntactically
		// broken mutant fails at import time and reads as a test failure.
		// --noEmit, since nothing here wants the output.
		return Invocation{Name: "npx", Args: []string{"tsc", "--noEmit"}}, true
	default:
		return Invocation{}, false
	}
}

func buildJest(variant Variant, args []string) (Invocation, bool) {
	switch variant {
	case Plain:
		return Invocation{Name: "npx", Args: append([]string{"jest"}, args...)}, true
	case Instrumented:
		return Invocation{
			Name:           "npx",
			Args:           append([]string{"jest", "--coverage"}, args...),
			CoverageReport: "coverage/lcov.info",
		}, true
	case BuildOnly:
		return Invocation{Name: "npx", Args: []string{"tsc", "--noEmit"}}, true
	default:
		return Invocation{}, false
	}
}
