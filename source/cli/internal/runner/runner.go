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
// Building a variant executes nothing: it returns argv, and the tests assert
// argv, matching internal/rust and internal/typescript — a unit test that
// shells out to a foreign toolchain tests the machine it runs on. Preparing a
// runner is the one thing here that does real work, because provisioning a
// pinned tool is not expressible as a command someone else runs.
package runner

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gotool"
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

// sourceExts is the file extensions each language's source is written in.
//
// The orphan gate reads this to decide whether a file is code some component
// ought to be testing, which is a question lydite can only ask about a
// language it has a runner for: a Python file is not code any component could
// claim, so demanding an exclude for one is paperwork for a question lydite
// cannot act on either way.
//
// It lives beside the Lang constants so the two cannot come apart. A language
// that gains a runner and no extensions is one whose files the gate is blind
// to, and TestEveryLangHasSourceExts refuses that.
var sourceExts = map[Lang][]string{
	Go:         {".go"},
	Rust:       {".rs"},
	TypeScript: {".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"},
}

// SourceExtsFor returns the extensions one language's source is written in,
// or nothing for a component whose runner implies no language. It is what
// scopes a diff to the files a component's coverage report could speak for,
// and it reads the same table the orphan gate does so the two cannot come
// apart.
func SourceExtsFor(l Lang) []string {
	return sourceExts[l]
}

// LangForExt returns the language a source file's extension belongs to, and
// whether it belongs to one at all. It reads the same table SourceExtsFor
// does, so a language that gains an extension gains it in both directions at
// once.
//
// Built once into a reverse map rather than ranging over the table, so the
// answer cannot depend on map iteration order. No extension belongs to two
// languages today and TestNoExtensionBelongsToTwoLanguages refuses one that
// does — but a reverse lookup that ranges would answer differently run to run
// in the window before anyone noticed, and orphan.Unscanned groups its warning
// by language, so the same tree would name a different one each time.
func LangForExt(ext string) (Lang, bool) {
	lang, ok := langByExt[ext]
	return lang, ok
}

// langByExt is sourceExts inverted. A duplicate extension would be lost here
// rather than answered inconsistently, which is what the test protects.
var langByExt = func() map[string]Lang {
	out := map[string]Lang{}
	for lang, exts := range sourceExts {
		for _, e := range exts {
			out[e] = lang
		}
	}
	return out
}()

// SourceExts returns every extension the languages lydite runs are written
// in, lowercase and dot-prefixed, sorted.
func SourceExts() []string {
	var out []string
	for _, exts := range sourceExts {
		out = append(out, exts...)
	}
	sort.Strings(out)
	return out
}

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
	// PathDirs is directories this command needs at the front of PATH: the
	// cache directory of a pinned tool, so the invocation stays the one a
	// reader could type — `cargo nextest run`, not an absolute path into a
	// version-keyed cache.
	//
	// Directories rather than a finished "PATH=..." entry, because a child's
	// environment is a flat list where the last occurrence of a key wins:
	// two callers each building their own PATH produce two entries and one
	// of them is discarded without a trace. toolchain.Compose is the single
	// place that turns every caller's directories into the one entry.
	PathDirs []string
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
	// Prepare puts in place what the runner needs before the named variant
	// will work, or is nil when it needs nothing.
	//
	// It lives on the runner rather than in the command so the command layer
	// carries no per-language branch — the thing this registry exists to
	// remove. dir is the component's directory, override is
	// typescript.install, and out is where a step's own output goes.
	//
	// It takes the invocation that is about to run, not the variant that
	// named it, because what a runner needs is a property of the command
	// rather than of the label: `cargo-llvm-cov-nextest`'s *plain* variant
	// runs through cargo-llvm-cov, and a rule keyed on the variant answers
	// "not instrumented" for the one invocation that needs the
	// instrumentation. Reading it off the command leaves one statement of
	// what each variant runs, in the function that builds it.
	//
	// Every runner needs one, for unrelated reasons. A JavaScript component
	// has no node_modules on a fresh checkout and every import fails before a
	// test is collected; a Rust component needs the pinned cargo-nextest,
	// which is not a degradation when absent but a component that cannot run
	// at all, and the tool config that turns its JUnit report on; a Go
	// component needs the pinned wrapper its instrumented variant runs
	// through. Its plain variant is `go test`, which needs nothing — the
	// toolchain fetches what a build needs on the way past — so this one is
	// read off the invocation like the rest.
	Prepare func(ctx context.Context, inv Invocation, dir, override string, env executil.Env, out io.Writer) error
}

// registry is the whole set, keyed by declared name.
var registry = map[Name]Runner{
	GoTest:              {Name: GoTest, Lang: Go, Build: buildGoTest, Prepare: installGoTestsum},
	CargoNextest:        {Name: CargoNextest, Lang: Rust, Build: buildCargoNextest, Prepare: installCargoTools},
	CargoLLVMCovNextest: {Name: CargoLLVMCovNextest, Lang: Rust, Build: buildCargoLLVMCovNextest, Prepare: installCargoTools},
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

// coverageDir is where a runner writes coverage, under ReportDir and never at
// it.
//
// A JavaScript runner is handed this directory and empties it before the run,
// and for a component rooted at the scan root ReportDir is where every
// component's log lives. Pointing a runner at the directory holding another
// component's output is a data-loss bug rather than an untidy layout.
var coverageDir = path.Join(ReportDir, "coverage")

// junitReport is where every runner that can be made to write one puts its
// JUnit XML, relative to the component directory.
//
// One path for every runner, because only one component can be running in a
// given directory tree at a time — the scheduler serialises components whose
// roots overlap, since two suites installing into and building in one tree is
// not a race either of them can report honestly — so there is nobody to
// collide with.
var junitReport = report("junit.xml")

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
		profile := path.Join(coverageDir, "coverage.out")
		// Through the pinned wrapper, because `go test` writes no report and
		// the ledger records test counts. Everything after `--` is the `go
		// test` command that would otherwise have run, unchanged, so what is
		// measured does not depend on the wrapper.
		//
		// Only the instrumented variant. The plain one is what mutation runs
		// once per mutant, where a JUnit report is written and discarded
		// thousands of times and the wrapper is a process in the way of the
		// thing being timed.
		return Invocation{
			Name: gotestsumName,
			Args: append([]string{
				"--format", "pkgname",
				"--junitfile", junitReport,
				"--",
				"-coverprofile=" + profile, "-coverpkg=./...",
			}, pkgs...),
			CoverageReport: profile,
			JUnitReport:    junitReport,
			PathDirs:       []string{gotestsumBinDir()},
		}, true
	case BuildOnly:
		return Invocation{Name: "go", Args: append([]string{"build"}, pkgs...)}, true
	default:
		return Invocation{}, false
	}
}

// rerunJUnitReport is where the flaky gate's second run writes its report,
// beside run 1's and never over it.
//
// Its own path because the ledger records run 1's counts: a rerun of five
// tests overwriting them would put "5 tests" in the quality history of a
// component that ran six hundred. One path for every package a component
// reruns, since the reruns are sequential and the caller reads each report
// before the next one starts — and the caller clears the path first, so a
// package whose rerun wrote nothing cannot be measured from the last one that
// did.
var rerunJUnitReport = report("flaky-rerun.xml")

// GoJUnitPlain is the plain Go suite run through the pinned wrapper, so it
// writes a JUnit report without being instrumented.
//
// The flaky gate reads a test's first outcome out of the report run 1 already
// wrote, and under --no-coverage the plain variant is `go test`, which writes
// none. Asking for the gate therefore has to make the run write one whichever
// variant it ran (ADR 0039).
//
// A function of its own and not a fourth Variant, because the wrapper must
// stay off the Plain variant itself: mutation runs Plain once per mutant, and
// a JUnit report written and discarded thousands of times is a process in the
// way of the thing being timed (ADR 0027). A variant is what every runner
// answers for; this is one language's plain invocation with one report added,
// asked for by the one caller that needs it.
func GoJUnitPlain(args []string) (Invocation, bool) {
	return Invocation{
		Name:        gotestsumName,
		Args:        append([]string{"--format", "pkgname", "--junitfile", junitReport, "--"}, goTestArgs(args)...),
		JUnitReport: junitReport,
		PathDirs:    []string{gotestsumBinDir()},
	}, true
}

// GoRerun is the flaky gate's second run: one package's named tests, filtered
// by an anchored -run pattern, in a process of their own.
//
// args is the component's declared `go test` arguments, which the rerun copies
// so the two runs differ in as little as possible — `-race` included, since a
// race detector present in one run and absent in the other makes two outcomes
// disagree for a reason that is not the test's. The coverage flags are dropped
// and the declared package patterns are replaced by pkg: a second profile
// written to the component's coverage.out would overwrite the measurement the
// coverage gate is about to read, and a rerun of the whole tree is not a rerun
// of the new tests.
//
// -run and -count=1 are appended after the declared flags so they win a
// duplicate, and -count=1 is the whole point of the invocation: run 2's argv
// is by construction a cacheable subset of a run that just happened, so
// without it Go serves run 1's own result and the gate reports determinism
// having executed nothing. That is the exact mirror of ADR 0027's rule that
// mutation must never pass -count=1, and neither is a mistake in the other's
// direction.
//
// It answers false for an empty name set. A rerun of no tests would be a
// `go test` with a pattern matching nothing, which prints `ok` and reports a
// pass for a filter that ran nothing at all.
func GoRerun(args []string, pkg string, names []string) (Invocation, bool) {
	if len(names) == 0 || pkg == "" {
		return Invocation{}, false
	}
	test := append(goTestFlags(args), "-run", RunPattern(names), "-count=1", pkg)
	return Invocation{
		Name:        gotestsumName,
		Args:        append([]string{"--format", "pkgname", "--junitfile", rerunJUnitReport, "--"}, test...),
		JUnitReport: rerunJUnitReport,
		PathDirs:    []string{gotestsumBinDir()},
	}, true
}

// RunPattern is the anchored alternation `go test -run` takes for an exact set
// of top-level test names.
//
// Anchored at both ends, so `-run TestFoo` cannot also select `TestFooBar`:
// the set the gate reruns has to be the set it decided was new, and a rerun
// that quietly widens it reports outcomes for tests nobody wrote. The names
// are spliced in unescaped, which is safe because they are Go identifiers —
// the only characters a compiler accepts in one are letters, digits and
// underscore, none of which mean anything to a regexp.
func RunPattern(names []string) string {
	return "^(" + strings.Join(names, "|") + ")$"
}

// goTestFlags is the declared arguments with the coverage flags and the
// package patterns removed, leaving what run 2 copies from run 1.
//
// A package pattern is an argument that is not a flag — the split cmd/go makes
// itself, where every `go test` flag begins with a dash and every package
// argument does not. Patterns are dropped wherever they sit rather than at the
// first one, since `go test ./... -v` is as ordinary a spelling as
// `go test -v ./...`.
//
// A flag may spell its value in the next argument as readily as after an
// equals sign, so goTestValueFlags says which ones do: dropping `-coverprofile`
// and leaving its path behind hands `go test` a path where it expects a
// package, and dropping `5m` out of `-timeout 5m` leaves a flag with no value.
// A flag that table does not name is taken to carry its value inline, which is
// the spelling that is unambiguous for every flag there is.
func goTestFlags(args []string) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
		value, separate := "", false
		if !inline && goTestValueFlags[name] && i+1 < len(args) {
			value, separate = args[i+1], true
			i++
		}
		if coverageFlags[name] {
			continue
		}
		out = append(out, a)
		if separate {
			out = append(out, value)
		}
	}
	return out
}

// coverageFlags is what run 2 must not carry. The profile is the measurement
// the coverage gate is about to read, and -coverpkg and -covermode without it
// are instrumentation the rerun pays for and nothing reads.
var coverageFlags = map[string]bool{
	"cover":        true,
	"coverprofile": true,
	"coverpkg":     true,
	"covermode":    true,
}

// goTestValueFlags is the `go test` and build flags whose value may be the
// argument after them. It is what keeps a flag and its value together when the
// rerun copies run 1's argv, and a boolean flag left out of it is right: only
// a flag that takes a value can swallow the argument behind it.
var goTestValueFlags = map[string]bool{
	"asmflags": true, "bench": true, "benchtime": true, "blockprofile": true,
	"blockprofilerate": true, "buildmode": true, "buildvcs": true, "count": true,
	"covermode": true, "coverpkg": true, "coverprofile": true, "cpu": true,
	"cpuprofile": true, "exec": true, "fuzz": true, "fuzzminimizetime": true,
	"fuzztime": true, "gcflags": true, "gocoverdir": true, "ldflags": true,
	"memprofile": true, "memprofilerate": true, "mod": true, "modfile": true,
	"mutexprofile": true, "mutexprofilefraction": true, "outputdir": true,
	"overlay": true, "p": true, "parallel": true, "run": true, "shuffle": true,
	"skip": true, "tags": true, "timeout": true, "toolexec": true, "trace": true,
}

// nextestJUnit is where cargo-nextest writes JUnit under the default profile.
//
// The path is nextest's own composition — the profile's directory plus the
// name the config gives — and lydite gives that name in the tool config it
// stages, so this is the file it asked for rather than one it hopes is there.
// A repository whose own configuration sets a different junit path wins, since
// a tool config sits below it in priority, and that component then contributes
// no test counts.
const nextestJUnit = "target/nextest/default/junit.xml"

// nextestRerunJUnit is where cargo-nextest writes JUnit under the rerun
// profile, which is the flaky gate's second run.
//
// Nextest composes a report's path from the profile's own directory and the
// name the config gives, so a run under `--profile rerun` lands beside run 1's
// report rather than over it. The name differs too, so the two stay
// distinguishable in a repository that points both profiles at one directory.
const nextestRerunJUnit = "target/nextest/rerun/junit-rerun.xml"

func buildCargoNextest(variant Variant, args []string) (Invocation, bool) {
	switch variant {
	case Plain:
		return Invocation{
			Name:     "cargo",
			Args:     append([]string{"nextest", "run"}, args...),
			PathDirs: cargoBinDirs(),
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
// One export, and it is the lcov. An lcov's summed line records give the same
// covered and total counts the JSON export's totals carry, so the aggregate is
// derivable from the lcov; the per-line hits the patch gate reads are not
// derivable from the JSON, which has no line data at all. Only one of the two
// is load-bearing, and asking for both is what produced an invocation carrying
// --output-path twice — which cargo-llvm-cov refuses to parse, before anything
// executes.
func llvmCovNextest(args []string) Invocation {
	lcov := path.Join(coverageDir, "lcov.info")
	inv := Invocation{
		Name:           "cargo",
		Args:           []string{"llvm-cov", "nextest", "--lcov", "--output-path", lcov},
		CoverageReport: lcov,
		PathDirs:       cargoBinDirs(),
	}
	askNextestJUnit(&inv, nextestJUnit)
	inv.Args = append(inv.Args, args...)
	return inv
}

// askNextestJUnit points the invocation at the staged tool config and names the
// report the named profile then writes, or reports false when there is nowhere
// to stage a config.
//
// The report is asked for rather than assumed: nextest writes one only where a
// profile says to, so an invocation that merely named the path would have
// lydite report a component unmeasured for a file nobody was ever told to
// write. A machine with no usable cache directory has nowhere to stage the
// config, and then there is no report and no claim of one either.
func askNextestJUnit(inv *Invocation, report string) bool {
	cfg, ok := nextestToolConfig()
	if !ok {
		return false
	}
	inv.Args = append(inv.Args, "--tool-config-file", "lydite:"+cfg)
	inv.JUnitReport = report
	return true
}

// CargoNextestJUnitPlain is the plain cargo-nextest suite with the JUnit report
// turned on, so it writes one without running through cargo-llvm-cov.
//
// It is GoJUnitPlain's counterpart and exists for the same reason: the flaky
// gate reads a test's first outcome out of the report run 1 already wrote, and
// under --no-coverage the plain variant asks for no report at all (ADR 0041).
// A function of its own and not a fourth Variant, because mutation runs Plain
// once per mutant and a report written and discarded thousands of times is work
// in the way of the thing being timed.
func CargoNextestJUnitPlain(args []string) (Invocation, bool) {
	inv := Invocation{
		Name:     "cargo",
		Args:     []string{"nextest", "run"},
		PathDirs: cargoBinDirs(),
	}
	askNextestJUnit(&inv, nextestJUnit)
	inv.Args = append(inv.Args, args...)
	return inv, true
}

// RustRerun is the flaky gate's second run for a Rust component: the named
// tests alone, under a profile whose report lands beside run 1's.
//
// One invocation per component and not per test binary. A rerun per binary
// would pay the crate's link step's worth of startup for each, for a gate whose
// whole budget is meant to be the new tests' own duration — so the filter is
// one OR-ed expression of exact predicates, and the caller hands this function
// one flat set of names.
//
// `test(=name)` matches a name and not a binary, so a name declared in two
// binaries reruns in both. That is the right answer for a gate — a name that is
// new in two binaries is two new tests — and it is legible only because the
// outcomes are read back by classname and name together.
//
// --no-fail-fast, because nextest stops at the first failure otherwise and a
// component with two failing new tests would have the second reported as though
// it never ran.
//
// The declared arguments are copied so the two runs differ in as little as
// possible, and the gate's own flags are appended after them so a declared
// duplicate cannot win. Nothing is stripped from them: Rust's instrumentation
// is a different runner rather than a flag, so a rerun built on plain
// `cargo nextest run` carries none of it.
//
// It answers false for an empty name set, for GoRerun's reason, and for an
// invocation that has nowhere to stage its tool config: without the config
// there is no rerun profile to select and no report to read, so the component's
// rerun is unmeasured rather than run blind.
func RustRerun(args []string, names []string) (Invocation, bool) {
	if len(names) == 0 {
		return Invocation{}, false
	}
	inv := Invocation{
		Name:     "cargo",
		Args:     []string{"nextest", "run"},
		PathDirs: cargoBinDirs(),
	}
	if !askNextestJUnit(&inv, nextestRerunJUnit) {
		return Invocation{}, false
	}
	inv.Args = append(inv.Args, args...)
	inv.Args = append(inv.Args, "--profile", "rerun", "--no-fail-fast", "-E", NextestFilter(names))
	return inv, true
}

// NextestFilter is the OR-ed expression of exact predicates cargo-nextest's -E
// takes for an exact set of test names.
//
// `=` is nextest's exact matcher, so a rerun of `doubles` cannot also select
// `doubles_deeper` — the set the gate reruns has to be the set it decided was
// new. The names are spliced in unescaped, which is safe because a Rust test's
// name is a path of identifiers joined with `::`, and nothing in one closes the
// predicate.
func NextestFilter(names []string) string {
	terms := make([]string, 0, len(names))
	for _, n := range names {
		terms = append(terms, "test(="+n+")")
	}
	return strings.Join(terms, " or ")
}

// installCargoTools installs the pinned cargo subcommands this invocation
// runs through, unless the cache already has those exact versions.
//
// cargo-llvm-cov is installed only for an invocation that actually runs it,
// because it is a multi-minute source build and a run that asked not to be
// instrumented must not pay for it. The question is asked of the command
// rather than of the variant that named it: `cargo-llvm-cov-nextest`'s plain
// variant runs through cargo-llvm-cov, so a rule keyed on the variant would
// leave that component failing with `no such command: llvm-cov`.
//
// The override is ignored: typescript.install describes a JavaScript
// workspace's own install flow, and there is no equivalent for a tool lydite
// pins — a repository able to substitute its own cargo-nextest would be back
// to a runner whose version varies by machine.
// installCargoTools installs *lydite's* pinned runners, so it gets the
// toolchain environment alone and nothing the scanned repository supplied.
// `cargo install` reads CARGO_HOME, CARGO_REGISTRIES_*, CARGO_NET_* and
// RUSTC_WRAPPER, so a declared environment reaching it would choose where
// lydite's own cargo-nextest and cargo-llvm-cov come from — and they are
// cached under a key naming the tool and its version, so one poisoned build
// outlives the run and, on a runner sharing ~/.cache/lydite, reaches other
// repositories. A repository may say how its own code builds; it may not say
// where lydite's tools come from.
func installCargoTools(ctx context.Context, inv Invocation, _, _ string, env executil.Env, out io.Writer) error {
	if err := cargoNextest.Install(ctx, env.Install, out); err != nil {
		return err
	}
	if err := stageNextestToolConfig(inv); err != nil {
		return err
	}
	if !runsLLVMCov(inv) {
		return nil
	}
	return cargoLLVMCov.Install(ctx, env.Install, out)
}

// stageNextestToolConfig writes the config the invocation was built to point
// at, and does nothing for one that points at none.
//
// Written every time rather than only when absent: it is a two-line file, and
// a stale one left by an older lydite would send the report somewhere this
// version does not read — which reads as a component whose suite ran no tests.
func stageNextestToolConfig(inv Invocation) error {
	if inv.JUnitReport == "" {
		return nil
	}
	cfg, ok := nextestToolConfig()
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o750); err != nil {
		return err
	}
	// Staged and renamed into place, never written over. The path is one file
	// for the whole process and components run concurrently, so a plain write
	// truncates a file another component's nextest may be reading as it
	// starts: a zero-length read leaves the JUnit profile off and the report
	// silently unwritten, and a partial one fails that component's suite on a
	// TOML parse error that has nothing to do with its code. Rename is atomic
	// within a directory, so a reader sees the whole of one version or the
	// whole of the other — the same stage-then-rename internal/download and
	// the toolchain installs already use.
	staged, err := os.CreateTemp(filepath.Dir(cfg), ".lydite-nextest-*.toml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	if _, err := staged.WriteString(nextestToolConfigBody); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(staged.Name(), cfg)
}

// installGoTestsum installs the pinned wrapper a Go component's instrumented
// suite runs through, and does nothing for the plain and build-only variants
// that do not.
//
// The question is asked of the built command and never of the variant that
// named it, which is the rule runsLLVMCov already follows: what a runner needs
// is a property of what it is about to run, and a second list of which
// variants use the wrapper is a list that is right until a variant changes and
// only one of the two is updated.
//
// It installs *lydite's* tool, so it gets the install environment alone and
// nothing the scanned repository declared — `go install` reads GOPROXY and
// GOSUMDB, and the cache key names the version rather than where it came from,
// so one substituted build outlives the run.
func installGoTestsum(ctx context.Context, inv Invocation, _, _ string, env executil.Env, _ io.Writer) error {
	if inv.Name != gotestsumName {
		return nil
	}
	_, err := gotool.Ensure(ctx, env.Install, gotestsumName, gotestsumVersion, gotestsumPkg, "")
	return err
}

// runsLLVMCov reports whether an invocation drives cargo-llvm-cov. It reads
// the built command, so what gets installed and what gets run cannot come
// apart — the alternative is a second list of which variants are instrumented,
// which is right until a runner changes and only one of the two is updated.
func runsLLVMCov(inv Invocation) bool {
	return inv.Name == "cargo" && len(inv.Args) > 0 && inv.Args[0] == "llvm-cov"
}

// cargoBinDirs is where the pinned cargo tools live.
//
// They go on PATH rather than being invoked by absolute path, because `cargo
// nextest` is how cargo finds a subcommand, and it is the invocation a reader
// can re-run from the failure detail — an absolute path into a version-keyed
// cache is neither. The caller prepends them, so an older copy already on the
// machine cannot win.
//
// Both tools' directories are returned whichever variant runs. The entry for
// a tool this variant does not invoke costs a directory nothing looks in, and
// splitting the list per variant would put the same construction in two
// places for that.
func cargoBinDirs() []string {
	var dirs []string
	for _, t := range []cargotool.Tool{cargoNextest, cargoLLVMCov} {
		if dir, err := t.BinDir(); err == nil {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// installNodeDeps runs the install internal/nodedeps resolves from the nearest
// lockfile at or above the component directory, or from the typescript.install
// override.
//
// Doing nothing is not a failure: a root no single lockfile identifies has
// nothing lydite can install without guessing, and guessing writes a lockfile
// the repository does not use.
// installNodeDeps installs the *repository's* dependencies, so it gets the
// environment the repository declared: a workspace whose install needs a
// registry or a token said so in its own declaration, and lydite is running
// that repository's package manager over that repository's lockfile.
//
// This is the opposite of installCargoTools below, and the difference is whose
// software is being fetched.
func installNodeDeps(ctx context.Context, _ Invocation, dir, override string, env executil.Env, out io.Writer) error {
	return nodedeps.Install(ctx, dir, declarationRoot(dir), override, env.Check, out)
}

// declarationRoot is the directory the walk for a workspace root may not climb
// above: the nearest ancestor of dir holding a .lydite directory, which is the
// root whose declaration put a component at dir in the first place.
//
// It is read off the tree rather than handed down, because the tree is the
// only thing that knows which root this dir belongs to. A mutation worker runs
// a component out of a copy of the scan root, so the root bounding its install
// is the copy and not the repository it was copied from — and a caller passing
// the repository would bound the walk by a directory the worker's dir is not
// under.
//
// A dir under no declaration at all is its own bound, which is the install a
// lockfile in the component directory alone resolves: nothing above a
// directory lydite was pointed at is ever installed from.
func declarationRoot(dir string) string {
	dir = filepath.Clean(dir)
	for d := dir; ; d = filepath.Dir(d) {
		if info, err := os.Stat(filepath.Join(d, config.Dir)); err == nil && info.IsDir() {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}

// buildVitest runs through the package manager's own binary directory rather
// than a global vitest, so the version the repository pinned is the version
// that runs.
func buildVitest(variant Variant, args []string) (Invocation, bool) {
	switch variant {
	case Plain:
		return Invocation{Name: "npx", Args: append([]string{"vitest", "run"}, args...)}, true
	case Instrumented:
		// The reporter and the directory are named rather than left to the
		// repository's own vitest config. lcov is the one format both gates
		// read, and vitest's default reporter set is text and html — a
		// component whose config says nothing would produce a coverage run
		// with no report lydite can parse, and report as unmeasured having
		// paid for the instrumentation.
		//
		// clean=false is not tidiness. Vitest empties its reports directory
		// before a run, and for a component rooted at the scan root that
		// directory is the one holding every component's log — including the
		// logs of components running concurrently beside it, whose failing
		// rows then name a file that no longer exists. The subdirectory alone
		// would fix it for every name but `coverage`; the flag fixes it for
		// all of them.
		instrumented := append([]string{
			"vitest", "run", "--coverage",
			"--coverage.reporter=lcovonly",
			"--coverage.reportsDirectory=" + coverageDir,
			"--coverage.clean=false",
		}, vitestJUnitArgs(junitReport)...)
		return Invocation{
			Name:           "npx",
			Args:           append(instrumented, args...),
			CoverageReport: path.Join(coverageDir, "lcov.info"),
			JUnitReport:    junitReport,
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

// vitestJUnitArgs is what turns vitest's JUnit report on, written once because
// the plain-with-JUnit run and the rerun differ only in where the report lands.
//
// The default reporter is named beside junit for the reason the instrumented
// variant names it: --reporter=junit alone replaces the reporter set, and the
// component's log would then hold nothing for a failing row to show.
func vitestJUnitArgs(report string) []string {
	return []string{"--reporter=default", "--reporter=junit", "--outputFile.junit=" + report}
}

// VitestJUnitPlain is the plain vitest suite with the JUnit report turned on
// and no instrumentation.
//
// GoJUnitPlain's counterpart, for the same reason and with the same shape: the
// flaky gate reads run 1's outcomes from the report the suite wrote, and under
// --no-coverage the plain variant writes none (ADR 0041). Not a fourth Variant,
// because mutation runs Plain once per mutant.
func VitestJUnitPlain(args []string) (Invocation, bool) {
	return Invocation{
		Name:        "npx",
		Args:        append(append([]string{"vitest", "run"}, vitestJUnitArgs(junitReport)...), args...),
		JUnitReport: junitReport,
	}, true
}

// VitestRerun is the flaky gate's second run for a TypeScript component: every
// file contributing a new test, named positionally, and one -t pattern covering
// every new test's full name.
//
// One invocation per component and not per file. A rerun per file would pay a
// fresh worker pool and module graph for each, for a gate whose whole budget is
// meant to be the new tests' own duration.
//
// Every metacharacter in a title is escaped, which is where this departs from
// RunPattern's unescaped splice: `-t` is a real regular expression matched
// against the full name, and a title is prose written by whoever wrote the
// test. An alternation built verbatim from `matches ^a (b) [c] + d$` does not
// match the title it was built from, and vitest reports that test skipped —
// which, under the gate's rule that a skip is an outcome, is a deterministic
// test reported as flaky by lydite's own filter.
//
// The report is the rerun's own and never run 1's, for GoRerun's reason: the
// ledger records run 1's counts, and a rerun of four tests overwriting them
// would put "4 tests" in the quality history of a component that ran six
// hundred.
//
// It answers false for an empty name set, for GoRerun's reason.
func VitestRerun(args []string, files []string, names []string) (Invocation, bool) {
	if len(names) == 0 {
		return Invocation{}, false
	}
	argv := append([]string{"vitest", "run"}, vitestJUnitArgs(rerunJUnitReport)...)
	argv = append(argv, vitestFlags(args)...)
	argv = append(argv, files...)
	argv = append(argv, "-t", TitlePattern(names))
	return Invocation{Name: "npx", Args: argv, JUnitReport: rerunJUnitReport}, true
}

// TitlePattern is the anchored alternation vitest's -t takes for an exact set
// of full test names, with every regex metacharacter in each name escaped.
//
// Anchored the way RunPattern is, so a rerun of `holds` cannot also select
// `holds a title`, and escaped because a vitest title is prose rather than an
// identifier.
func TitlePattern(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, regexp.QuoteMeta(n))
	}
	return RunPattern(quoted)
}

// vitestFlags is the declared arguments with the flags run 2 supplies itself
// removed.
//
// The coverage flags go for the reason Go's do: the rerun must not overwrite
// the measurement the coverage gate is about to read, nor pay for
// instrumentation nothing reads. The reporter, the report path and any declared
// test-name pattern go because the rerun states its own, and vitest resolves a
// repeated --reporter by accumulating rather than replacing — a declared one
// would add a reporter to a run whose output the gate parses.
//
// A flag may spell its value in the next argument as readily as after an equals
// sign, so vitestValueFlag says which ones do: dropping --outputFile.junit and
// leaving its path behind hands vitest a path where it expects a file filter.
// A positional argument is kept, since it is the component's own file filter
// and it constrained run 1 just as it constrains this one.
func vitestFlags(args []string) []string {
	out := []string{}
	// Walked by reslicing rather than by an index skipped ahead: a value
	// flag's own value is never read here, only stepped past, and a check
	// against the wrong boundary would read args past its end rather than
	// silently doing nothing — a real consequence a test can observe, where
	// an index that merely overshoots a length nothing indexes with cannot be
	// told from one that stopped exactly on it.
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		name, _, inline := strings.Cut(a, "=")
		if !vitestRerunSupplies(name) {
			out = append(out, a)
			continue
		}
		if !inline && vitestValueFlag(name) && len(args) > 0 {
			args = args[1:]
		}
	}
	return out
}

// vitestRerunSupplies reports whether the rerun states this flag itself, so a
// declared one is dropped rather than carried into run 2.
func vitestRerunSupplies(name string) bool {
	return name == "--coverage" || vitestValueFlag(name)
}

// vitestValueFlag reports whether a flag the rerun supplies may carry its value
// in the argument after it, which is what keeps a dropped flag from leaving its
// value behind as a file filter.
func vitestValueFlag(name string) bool {
	switch name {
	case "--reporter", "--outputFile.junit", "-t", "--testNamePattern":
		return true
	}
	return strings.HasPrefix(name, "--coverage.")
}

func buildJest(variant Variant, args []string) (Invocation, bool) {
	switch variant {
	case Plain:
		return Invocation{Name: "npx", Args: append([]string{"jest"}, args...)}, true
	case Instrumented:
		// Named for the reason vitest's are: jest's default reporters do not
		// include lcov, and a coverage run whose report lydite cannot parse
		// costs the instrumentation and measures nothing. Its own
		// subdirectory for the reason vitest's is, so a runner that empties
		// what it is pointed at cannot reach the component logs.
		return Invocation{
			Name: "npx",
			Args: append([]string{
				"jest", "--coverage",
				"--coverageReporters=lcovonly",
				"--coverageDirectory=" + coverageDir,
			}, args...),
			CoverageReport: path.Join(coverageDir, "lcov.info"),
		}, true
	case BuildOnly:
		return Invocation{Name: "npx", Args: []string{"tsc", "--noEmit"}}, true
	default:
		return Invocation{}, false
	}
}

// Producer names what wrote a component's coverage report, for the baseline to
// record beside the counts.
//
// It exists because a coverage figure is only comparable to one taken by the
// same instrument. A runner or coverage-provider bump changes what a line is:
// vitest 3.2.7 to 4.1.11 took one workspace from 345 lines to 152 over an
// identical tree, and the gate reported the fall as a regression by whoever
// bumped it. Recorded, the difference reports the component new instead.
//
// It lives here because this is where the instrumented variants are built and
// where the pins those variants run through are read, so the name and the
// version can never come from a different place than the invocation does. dir
// is the component's directory, and lang is the resolved language toolchain —
// the Go toolchain that wrote a profile, or the Rust toolchain whose LLVM
// wrote an lcov.
//
// An empty answer means lydite could not identify the instrument, which is
// possible only for JavaScript: it is the one language whose measuring tool
// lydite deliberately does not pin, because installing one into the tree it is
// about to gate would have lydite change what the repository resolves to.
func (r Runner) Producer(dir, lang string) string {
	switch r.Name {
	case GoTest:
		// The profile is the toolchain's own output; nothing else is
		// involved, so there is no second version to name.
		return join("go", lang)
	case CargoNextest, CargoLLVMCovNextest:
		// Both instrumented variants run through cargo-llvm-cov, and its
		// line records follow the LLVM in the toolchain that built them —
		// so the pair is the instrument, not either half.
		return both(join("cargo-llvm-cov", cargoLLVMCov.Version), join("rust", lang))
	case Vitest:
		return jsProducer(dir, "vitest", "@vitest/coverage-v8", "@vitest/coverage-istanbul")
	case Jest:
		// Jest instruments through babel-plugin-istanbul, which it bundles,
		// so the runner's own version is the whole of the answer.
		return jsProducer(dir, "jest")
	default:
		return ""
	}
}

// jsProducer names the installed runner and, where one is separate, the
// coverage provider beside it.
//
// Every named package must be identifiable or the answer is empty: a producer
// naming half of what measured is one that compares equal across a change to
// the half it left out, which is worse than admitting it does not know. A
// provider is looked for in order and the first one installed wins, since a
// workspace carries the one its config selects.
func jsProducer(dir, run string, providers ...string) string {
	version, ok := nodedeps.PackageVersion(dir, run)
	if !ok {
		return ""
	}
	out := join(run, version)
	if len(providers) == 0 {
		return out
	}
	for _, p := range providers {
		if v, ok := nodedeps.PackageVersion(dir, p); ok {
			return both(out, join(p, v))
		}
	}
	return ""
}

// both names two halves of one instrument, or nothing when either is unknown.
// A producer naming half of what measured compares equal to itself across a
// change to the half it omitted, which is the comparison this exists to
// prevent — so an incomplete answer is no answer.
func both(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	return a + ", " + b
}

// join names a tool and its version, or nothing at all when the version is
// unknown — a bare tool name would compare equal to itself across every
// version of it, which is the comparison a producer exists to prevent.
func join(name, version string) string {
	if version == "" {
		return ""
	}
	return name + " " + version
}
