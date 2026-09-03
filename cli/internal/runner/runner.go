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
	"path"
	"sort"

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/executil"
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
	// Two runners need one, for unrelated reasons. A JavaScript component has
	// no node_modules on a fresh checkout and every import fails before a test
	// is collected; a Rust component needs the pinned cargo-nextest, which is
	// not a degradation when absent but a component that cannot run at all.
	// `go test` needs nothing — its toolchain fetches what a build needs on
	// the way past.
	Prepare func(ctx context.Context, inv Invocation, dir, override string, env executil.Env, out io.Writer) error
}

// registry is the whole set, keyed by declared name.
var registry = map[Name]Runner{
	GoTest:              {Name: GoTest, Lang: Go, Build: buildGoTest},
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
			PathDirs:    cargoBinDirs(),
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
	return Invocation{
		Name:           "cargo",
		Args:           append([]string{"llvm-cov", "nextest", "--lcov", "--output-path", lcov}, args...),
		CoverageReport: lcov,
		JUnitReport:    nextestJUnit,
		PathDirs:       cargoBinDirs(),
	}
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
	if !runsLLVMCov(inv) {
		return nil
	}
	return cargoLLVMCov.Install(ctx, env.Install, out)
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

// installNodeDeps runs the install internal/nodedeps resolves from the
// component root's lockfile, or from the typescript.install override.
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
	return nodedeps.Install(ctx, dir, override, env.Check, out)
}

// buildVitest runs through the package manager's own binary directory rather
// than a global vitest, so the version the repository pinned is the version
// that runs.
func buildVitest(variant Variant, args []string) (Invocation, bool) {
	switch variant {
	case Plain:
		return Invocation{Name: "npx", Args: append([]string{"vitest", "run"}, args...), JUnitReport: report("junit.xml")}, true
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
		return Invocation{
			Name: "npx",
			Args: append([]string{
				"vitest", "run", "--coverage",
				"--coverage.reporter=lcovonly",
				"--coverage.reportsDirectory=" + coverageDir,
				"--coverage.clean=false",
			}, args...),
			CoverageReport: path.Join(coverageDir, "lcov.info"),
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
