package crap

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/runner"
)

// scored is every function one probe file scores, keyed by name, with the whole
// file reported as covered so a test whose subject is the walk does not have to
// state coverage line by line.
func scored(t *testing.T, lang runner.Lang, tree, rel string) map[string]Function {
	t.Helper()
	out, err := scoreTree(lang, tree, rel, covering(400, 1))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Function{}
	for _, f := range out.scored {
		byName[f.Name] = f
	}
	if len(byName) != len(out.scored) {
		t.Fatalf("%s: two functions share a name: %v", rel, out.scored)
	}
	return byName
}

func assertComplexity(t *testing.T, rel string, got map[string]Function, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: scored %d functions, want %d: %v", rel, len(got), len(want), names(got))
	}
	for name, n := range want {
		f, ok := got[name]
		if !ok {
			t.Errorf("%s: %s was not scored; scored %v", rel, name, names(got))
			continue
		}
		if f.Complexity != n {
			t.Errorf("%s: %s scored complexity %d, want %d", rel, name, f.Complexity, n)
		}
	}
}

func names(got map[string]Function) []string {
	out := make([]string, 0, len(got))
	for name := range got {
		out = append(out, name)
	}
	return out
}

// Every Rust counting rule ADR 0036 names, one function per shape, against the
// numbers derived by hand in testdata/README.md. Three of them are numbers
// `rust-code-analysis` disagrees with, and the README says why lydite keeps its
// own: `describe` does not count the `_` arm, `tally` folds its closure in
// rather than scoring it apart, and `outer` keeps a flat one for the nested
// `fn` the oracle only scores separately.
func TestTheRustWalkCountsWhatADR0036Predicts(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("testdata", "rustprobe"))
	assertComplexity(t, "src/lib.rs", scored(t, runner.Rust, tree, "src/lib.rs"), map[string]int{
		"classify":       4,
		"guarded":        4,
		"unwrap_or_zero": 2,
		"drain":          2,
		"countdown":      2,
		"first_even":     3,
		"poll_until":     3,
		"describe":       4,
		"label":          3,
		"parse_pair":     3,
		"tally":          2,
		"outer":          3,
		"inner":          2,
	})
}

// Every TypeScript counting rule, likewise. `evens` is the one number ESLint
// disagrees with: it scores every arrow apart, where lydite folds a callback
// into the function whose span contains it.
func TestTheTypeScriptWalkCountsWhatADR0036Predicts(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("testdata", "tsprobe"))
	assertComplexity(t, "src/probe.ts", scored(t, runner.TypeScript, tree, "src/probe.ts"), map[string]int{
		"pick":       2,
		"title":      3,
		"notify":     2,
		"settle":     4,
		"readNumber": 2,
		"describe":   3,
		"greet":      3,
		"total":      4,
		"evens":      2,
		"bucket":     3,
		"Gate.allow": 4,
	})
	// TSX reads under its own tables, and every node type the counting rules
	// name is spelled identically in both.
	assertComplexity(t, "src/badge.tsx", scored(t, runner.TypeScript, tree, "src/badge.tsx"), map[string]int{
		"Badge": 4,
	})
}

// A nested named function is scored in its own right, so its lines are evidence
// about it and leave the span of the function containing it. Without that, a
// well-tested nested function would carry its parent's coverage figure and the
// parent would be scored against lines it does not contain the branches of.
func TestANestedFunctionsLinesLeaveItsParentsSpan(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("testdata", "rustprobe"))
	// Only `inner`'s lines are covered, out of a report that knows every line
	// of the file.
	hits := covering(200, 0)
	for line := 116; line <= 122; line++ {
		hits[line] = 1
	}
	out, err := scoreTree(runner.Rust, tree, "src/lib.rs", hits)
	if err != nil {
		t.Fatal(err)
	}
	var outer, inner Function
	for _, f := range out.scored {
		switch f.Name {
		case "outer":
			outer = f
		case "inner":
			inner = f
		}
	}
	if inner.Lines.Percent() != 100 {
		t.Errorf("inner is %v%% covered, want the lines the report covers", inner.Lines.Percent())
	}
	if outer.Lines.Covered != 0 {
		t.Errorf("outer counts %d covered line(s), want the nested function's own out of its span",
			outer.Lines.Covered)
	}
	// `outer` spans 115–128 and `inner` 116–122, of which the report knows
	// every line: fourteen less seven.
	if inner.Lines.Total != 7 || outer.Lines.Total != 7 {
		t.Errorf("outer counts %d line(s) and inner %d, want seven each — outer's fourteen less inner's",
			outer.Lines.Total, inner.Lines.Total)
	}
}

// A declared function is not scored and is counted, in both languages, for the
// reason the Go path counts one: a repository can annotate its way to nothing
// above the threshold, and this number is what makes it visible when one does.
func TestADeclaredRustOrTypeScriptFunctionIsNotScoredAndIsCounted(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		file string
		src  string
	}{
		{runner.Rust, "src/lib.rs", `// scored branches.
pub fn scored(n: i64) -> i64 {
    if n > 0 {
        1
    } else {
        0
    }
}

// declared provisions something.
// [lydite:exclude_from_crap][the proving ground exercises this end to end]
pub fn declared(n: i64) -> i64 {
    if n > 0 {
        1
    } else {
        0
    }
}
`},
		{runner.TypeScript, "src/a.ts", `// scored branches.
export function scored(n: number): number {
  return n > 0 ? 1 : 0;
}

// declared provisions something.
// [lydite:exclude_from_crap][the proving ground exercises this end to end]
export function declared(n: number): number {
  return n > 0 ? 1 : 0;
}
`},
	} {
		root := t.TempDir()
		write(t, root, c.file, c.src)
		out, err := scoreTree(c.lang, root, c.file, covering(60, 1))
		if err != nil {
			t.Fatal(err)
		}
		if out.excluded != 1 {
			t.Errorf("%s: excluded = %d, want the declared function counted", c.file, out.excluded)
		}
		if len(out.scored) != 1 || out.scored[0].Name != "scored" {
			t.Errorf("%s: scored %v, want the declared one left out", c.file, out.scored)
		}
	}
}

// A coverage declaration excludes a function here too, and is counted. Its
// lines are already gone from the hit map, so the function would score nothing
// measurable and drop out in silence — uncounted, which is the one thing the
// excluded count exists to prevent.
func TestAFunctionExcludedFromCoverageIsCountedInEveryLanguage(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/lib.rs", `// provision fetches a toolchain.
// [lydite:exclude_from_coverage][the proving ground exercises this end to end]
pub fn provision(n: i64) -> i64 {
    if n > 0 {
        1
    } else {
        0
    }
}

// scored is measured.
pub fn scored(n: i64) -> i64 {
    n + 1
}
`)
	// The hit map is what internal/coverage would hand over: the declared
	// function's lines are already gone from it.
	hits := covering(20, 1)
	for line := 3; line <= 9; line++ {
		delete(hits, line)
	}
	out, err := scoreTree(runner.Rust, root, "src/lib.rs", hits)
	if err != nil {
		t.Fatal(err)
	}
	if out.excluded != 1 {
		t.Errorf("excluded = %d, want the coverage-declared function counted", out.excluded)
	}
	if len(out.scored) != 1 || out.scored[0].Name != "scored" {
		t.Errorf("scored %v, want only the undeclared function", out.scored)
	}
}

// A declaration that documents no function is carried back so the run can name
// it, and only the CRAP gate's own are: a coverage declaration documenting
// nothing is the coverage gate's warning to give, and giving it twice would
// have one typo reported by two gates.
func TestADeclarationCoveringNoRustFunctionIsCarriedBack(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/lib.rs", `pub fn f(n: i64) -> i64 {
    // [lydite:exclude_from_crap][written inside the body, where it does nothing]
    n
}

pub fn g(n: i64) -> i64 {
    // [lydite:exclude_from_coverage][the other gate's to diagnose]
    n
}
`)
	out, err := scoreTree(runner.Rust, root, "src/lib.rs", covering(20, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.unused) != 1 || out.unused[0] != "src/lib.rs:2" {
		t.Errorf("unused = %v, want the CRAP declaration alone, located", out.unused)
	}
	if out.excluded != 0 {
		t.Errorf("excluded = %d, want nothing excluded by a declaration that documents no function", out.excluded)
	}
}

// Test code is scored by nothing, whether a whole file recognised by its path
// or a `#[cfg(test)]` module inside one that is scored. Rust's lcov and
// TypeScript's coverage reports both describe test code, where a Go profile
// never has, so the rule Go gets from its toolchain is an explicit one here.
func TestTestCodeIsNotScored(t *testing.T) {
	for _, c := range []struct {
		lang  runner.Lang
		probe string
		rel   string
	}{
		{runner.Rust, "rustprobe", "tests/integration.rs"},
		{runner.Rust, "rustprobe", "benches/throughput.rs"},
		{runner.TypeScript, "tsprobe", "src/probe.test.ts"},
		{runner.TypeScript, "tsprobe", "src/gate.spec.ts"},
		{runner.TypeScript, "tsprobe", "src/__tests__/helpers.ts"},
	} {
		tree := fixture.Tree(t, filepath.Join("testdata", c.probe))
		out, err := scoreTree(c.lang, tree, c.rel, covering(400, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(out.scored) != 0 {
			t.Errorf("%s: scored %v, want nothing", c.rel, out.scored)
		}
	}
	// And the module inside the file that is scored: `classify_names_each_band`
	// is a function the walk would reach, with branches of its own.
	tree := fixture.Tree(t, filepath.Join("testdata", "rustprobe"))
	for name := range scored(t, runner.Rust, tree, "src/lib.rs") {
		if strings.HasSuffix(name, "_names_each_band") || strings.HasPrefix(name, "outer_folds") {
			t.Errorf("%s was scored, want the `#[cfg(test)]` module pruned", name)
		}
	}
}

// Measure reads a Rust and a TypeScript component end to end, the way it reads
// a Go one: the hit map bounds what is scored, the report names what is over
// the threshold, and Worst is over every function scored.
func TestMeasureScoresRustAndTypeScriptBesideGo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n\nfunc simple() int { return 1 }\n")
	write(t, root, "src/lib.rs", `pub fn tangled(n: i64) -> i64 {
    if n > 1 && n < 9 || n == 99 {
        return 1;
    }
    match n {
        0 => 1,
        1 => 2,
        _ => 3,
    }
}
`)
	write(t, root, "web/app.ts", `export function tangled(n: number): number {
  if (n > 1 && (n < 9 || n === 99)) {
    return 1;
  }
  switch (n) {
    case 0:
      return 1;
    default:
      return (n ?? 0) > 2 ? 2 : 3;
  }
}
`)
	// Nothing covered, so every function scores comp² + comp: the Rust
	// function is complexity 6 at 42 and the TypeScript one 7 at 56.
	rep, err := Measure(root, coverage.LineHits{
		"a.go":        covering(4, 0),
		"src/lib.rs":  covering(20, 0),
		"web/app.ts":  covering(20, 0),
		"notes/x.txt": covering(20, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 3 {
		t.Fatalf("scored %d functions, want one per source file: %+v", rep.Scored, rep.Over)
	}
	if rep.Above() != 2 {
		t.Fatalf("above = %d, want the two tangled functions: %+v", rep.Above(), rep.Over)
	}
	if rep.Over[0].File != "web/app.ts" || rep.Over[1].File != "src/lib.rs" {
		t.Errorf("over = %+v, want the worst first", rep.Over)
	}
	if rep.Worst != rep.Over[0].Value {
		t.Errorf("worst = %v, want the highest scored value %v", rep.Worst, rep.Over[0].Value)
	}
}

// A file the report names and lydite cannot read or parse is an error naming
// it, never a file quietly skipped — the same rule the Go path follows, since a
// report short one file is a count the gate compares against a baseline taken
// over all of them.
func TestARustOrTypeScriptFileThatCannotBeReadIsAnErrorNamingIt(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"src/gone.rs", "src/gone.ts"} {
		if _, err := Measure(root, coverage.LineHits{file: covering(3, 1)}); err == nil ||
			!strings.Contains(err.Error(), file) {
			t.Errorf("err = %v, want one naming %s", err, file)
		}
	}
	write(t, root, "src/broken.rs", "pub fn f( {\n")
	write(t, root, "src/broken.ts", "export function f( {\n")
	for _, file := range []string{"src/broken.rs", "src/broken.ts"} {
		if _, err := Measure(root, coverage.LineHits{file: covering(3, 1)}); err == nil ||
			!strings.Contains(err.Error(), file) {
			t.Errorf("err = %v, want one naming %s", err, file)
		}
	}
}

// An extension no walk table names returns the empty language, not merely a
// false bool — a mutant that swapped the empty string for anything else would
// pass every test that only checked the bool half of tracked's answer.
func TestAnUnwalkedExtensionTracksAsTheEmptyLanguage(t *testing.T) {
	lang, walked := tracked("src/component.py")
	if walked {
		t.Fatalf("walked = true for a .py file, want false")
	}
	if lang != "" {
		t.Errorf("lang = %q for an untracked extension, want the empty string", lang)
	}
}

// .mts and .cts parse under the same grammar .ts and .tsx already do, and are
// walked rather than silently dropped or reported as skipped.
func TestMtsAndCtsAreWalkedAsTypeScript(t *testing.T) {
	for _, file := range []string{"src/a.mts", "src/a.cts"} {
		lang, walked := tracked(file)
		if !walked || lang != runner.TypeScript {
			t.Errorf("tracked(%q) = %q, %v, want typescript, true", file, lang, walked)
		}
		if skipped(file) {
			t.Errorf("skipped(%q) = true, want false — it is walked", file)
		}
	}
}

// A script is a language no runner runs, so this gate has nothing to score and
// nothing to report as skipped.
func TestAScriptPathIsNotReportedSkipped(t *testing.T) {
	for _, file := range []string{"a.py", "a.sh", "a.bash"} {
		if skipped(file) {
			t.Errorf("skipped(%q) = true, want false", file)
		}
	}
}

// A .jsx or .js file is TypeScript by runner.LangForExt's own table, but this
// gate cannot walk it — JSX parses badly under the TypeScript grammar — so it
// is neither silently dropped nor scored as though it were absent: Measure
// names it in Report.Skipped.
func TestAJSXFileIsNamedSkippedRatherThanSilentlyDropped(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/plain.ts", "export function f(a: number): number {\n  return a\n}\n")
	write(t, root, "src/zebra.jsx", "export function Zebra() {\n  return 1\n}\n")
	write(t, root, "src/apple.jsx", "export function Apple() {\n  return 1\n}\n")
	rep, err := Measure(root, coverage.LineHits{
		"src/plain.ts":  covering(3, 3),
		"src/zebra.jsx": covering(3, 3),
		"src/apple.jsx": covering(3, 3),
	})
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if rep.Scored != 1 {
		t.Fatalf("scored = %d, want 1 (only the .ts file): %+v", rep.Scored, rep.Over)
	}
	// Sorted, the same reason Measure sorts the files it scores: a run over
	// one tree produces one report, not one that reads differently depending
	// on map iteration order.
	want := []string{"src/apple.jsx", "src/zebra.jsx"}
	if !slices.Equal(rep.Skipped, want) {
		t.Errorf("skipped = %v, want %v (sorted)", rep.Skipped, want)
	}
}

// A component whose every file is skipped is not scored, and the report says
// which is why: not "no function to score", which would read the same as an
// empty component, but named as unwalked.
func TestAllFilesSkippedIsNotConfusedWithNothingToScore(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/widget.jsx", "export function Widget() {\n  return 1\n}\n")
	rep, err := Measure(root, coverage.LineHits{"src/widget.jsx": covering(3, 3)})
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if rep.Scored != 0 {
		t.Fatalf("scored = %d, want 0", rep.Scored)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "src/widget.jsx" {
		t.Errorf("skipped = %v, want [src/widget.jsx]", rep.Skipped)
	}
}
