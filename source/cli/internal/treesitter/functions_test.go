package treesitter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/runner"
)

// unit is one expected row: the name, the span, and how many units nested in
// it are scored on their own.
type unit struct {
	name   string
	span   Span
	nested int
}

// probe enumerates one file of a materialised probe tree. The path handed to
// the enumerator is relative to the probe's own root, because that is what the
// test-code conventions read.
func probe(t *testing.T, lang runner.Lang, tree, rel string) []Func {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	funcs, language, err := ScoredFunctions(lang, rel, src)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	if len(funcs) > 0 && language == nil {
		t.Fatalf("%s: functions came back without the tables they were read under", rel)
	}
	return funcs
}

func assertUnits(t *testing.T, rel string, got []Func, want []unit) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d functions, want %d: %v", rel, len(got), len(want), names(got))
	}
	for i, w := range want {
		switch {
		case got[i].Name != w.name:
			t.Errorf("%s: function %d is %q, want %q", rel, i, got[i].Name, w.name)
		case got[i].Span != w.span:
			t.Errorf("%s: %s spans %+v, want %+v", rel, w.name, got[i].Span, w.span)
		case len(got[i].Nested) != w.nested:
			t.Errorf("%s: %s has %d nested unit(s), want %d", rel, w.name, len(got[i].Nested), w.nested)
		case got[i].Node == nil:
			t.Errorf("%s: %s came back with no node to walk", rel, w.name)
		}
	}
}

func names(funcs []Func) []string {
	out := make([]string, 0, len(funcs))
	for _, f := range funcs {
		out = append(out, f.Name)
	}
	return out
}

// The spans and the nesting the Rust probe predicts, cross-checked by hand
// against its source in internal/crap/testdata/README.md. A closure's lines
// stay in the function declaring them; a nested `fn` leaves its parent's count
// of one behind and is scored in its own right.
func TestTheRustProbeEnumeratesEveryScoredFunction(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "crap", "testdata", "rustprobe"))
	assertUnits(t, "src/lib.rs", probe(t, runner.Rust, tree, "src/lib.rs"), []unit{
		{name: "classify", span: Span{First: 7, Last: 17}},
		{name: "guarded", span: Span{First: 20, Last: 26}},
		{name: "unwrap_or_zero", span: Span{First: 30, Last: 36}},
		{name: "drain", span: Span{First: 39, Last: 45}},
		{name: "countdown", span: Span{First: 48, Last: 55}},
		{name: "first_even", span: Span{First: 58, Last: 65}},
		{name: "poll_until", span: Span{First: 69, Last: 77}},
		{name: "describe", span: Span{First: 80, Last: 87}},
		{name: "label", span: Span{First: 91, Last: 96}},
		{name: "parse_pair", span: Span{First: 100, Last: 105}},
		{name: "tally", span: Span{First: 108, Last: 111}},
		{name: "outer", span: Span{First: 115, Last: 128}, nested: 1},
		{name: "inner", span: Span{First: 116, Last: 122}},
	})
}

// `outer` is told about `inner` directly, so nothing downstream re-walks the
// tree to discover that its span holds a unit of its own.
func TestANestedRustFnIsReportedToItsParent(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "crap", "testdata", "rustprobe"))
	funcs := probe(t, runner.Rust, tree, "src/lib.rs")
	outer := funcs[len(funcs)-2]
	inner := funcs[len(funcs)-1]
	if len(outer.Nested) != 1 {
		t.Fatalf("outer has %d nested unit(s), want 1", len(outer.Nested))
	}
	if outer.Nested[0].Span != inner.Span || outer.Nested[0].Node != inner.Node {
		t.Errorf("outer's nested unit is %+v, want inner at %+v", outer.Nested[0].Span, inner.Span)
	}
}

// The spans the TypeScript probe predicts. `evens` holds a callback arrow that
// folds into it — no row of its own — where `bucket`, an arrow written as a
// declaration at the top level, is a row.
func TestTheTypeScriptProbeEnumeratesEveryScoredFunction(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "crap", "testdata", "tsprobe"))
	assertUnits(t, "src/probe.ts", probe(t, runner.TypeScript, tree, "src/probe.ts"), []unit{
		{name: "pick", span: Span{First: 8, Last: 10}},
		{name: "title", span: Span{First: 13, Last: 15}},
		{name: "notify", span: Span{First: 18, Last: 20}},
		{name: "settle", span: Span{First: 23, Last: 27}},
		{name: "readNumber", span: Span{First: 30, Last: 36}},
		{name: "describe", span: Span{First: 39, Last: 48}},
		{name: "greet", span: Span{First: 51, Last: 53}},
		{name: "total", span: Span{First: 56, Last: 69}},
		{name: "evens", span: Span{First: 72, Last: 74}},
		{name: "bucket", span: Span{First: 77, Last: 82}},
		{name: "Gate.allow", span: Span{First: 88, Last: 93}},
	})
	assertUnits(t, "src/badge.tsx", probe(t, runner.TypeScript, tree, "src/badge.tsx"), []unit{
		{name: "Badge", span: Span{First: 7, Last: 15}},
	})
}

// The spans and the nesting the Python probe predicts. A decorated function's
// span is the `def`'s own, the decorator line above it belonging to no unit —
// the same place a Rust `#[inline]` line sits. A nested `def` is a unit of its
// own, a `lambda` folds into the function holding it, and a method is named by
// the class it is written on.
func TestThePythonProbeEnumeratesEveryScoredFunction(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "crap", "testdata", "pyprobe"))
	assertUnits(t, "src/probe.py", probe(t, runner.Python, tree, "src/probe.py"), []unit{
		{name: "classify", span: Span{First: 10, Last: 19}},
		{name: "guarded", span: Span{First: 22, Last: 26}},
		{name: "pick", span: Span{First: 29, Last: 31}},
		{name: "countdown", span: Span{First: 34, Last: 40}},
		{name: "first_even", span: Span{First: 43, Last: 48}},
		{name: "search", span: Span{First: 51, Last: 57}},
		{name: "read_number", span: Span{First: 60, Last: 65}},
		{name: "read_pair", span: Span{First: 68, Last: 79}},
		{name: "regroup", span: Span{First: 82, Last: 90}},
		{name: "evens", span: Span{First: 93, Last: 95}},
		{name: "tally", span: Span{First: 98, Last: 101}},
		{name: "require_positive", span: Span{First: 104, Last: 107}},
		{name: "read_file", span: Span{First: 110, Last: 113}},
		{name: "describe", span: Span{First: 116, Last: 124}},
		{name: "label", span: Span{First: 127, Last: 133}},
		{name: "bracket", span: Span{First: 136, Last: 144}},
		{name: "gather", span: Span{First: 147, Last: 154}},
		{name: "cached_band", span: Span{First: 158, Last: 162}},
		{name: "outer", span: Span{First: 165, Last: 175}, nested: 1},
		{name: "inner", span: Span{First: 168, Last: 171}},
		{name: "Gate.__init__", span: Span{First: 181, Last: 182}},
		{name: "Gate.allow", span: Span{First: 184, Last: 187}},
		{name: "Marker.kind", span: Span{First: 195, Last: 196}},
	})
}

// Test code is not scored, and a whole file of it yields nothing rather than
// an error: it is a file with nothing to score, not one lydite could not read.
func TestTestCodeIsNotEnumerated(t *testing.T) {
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
		{runner.Python, "pyprobe", "src/test_probe.py"},
		{runner.Python, "pyprobe", "src/gate_test.py"},
		{runner.Python, "pyprobe", "src/tests/helpers.py"},
	} {
		tree := fixture.Tree(t, filepath.Join("..", "crap", "testdata", c.probe))
		if got := probe(t, c.lang, tree, c.rel); len(got) != 0 {
			t.Errorf("%s: scored %v, want nothing", c.rel, names(got))
		}
	}
}

// A `#[cfg(test)] mod` sits inside the file it tests, so no path rule can see
// it: the walk is pruned at the module rather than filtered afterwards.
func TestACfgTestModuleIsPrunedFromTheWalk(t *testing.T) {
	src := "pub fn ships() -> i64 {\n    1\n}\n\n" +
		"#[cfg(test)]\nmod tests {\n    #[test]\n    fn it_ships() {\n        assert_eq!(ships(), 1);\n    }\n}\n"
	funcs, _, err := ScoredFunctions(runner.Rust, "src/lib.rs", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	assertUnits(t, "src/lib.rs", funcs, []unit{{name: "ships", span: Span{First: 1, Last: 3}}})
}

// A module behind a broader condition is not the suite. The rule fails open:
// a form it does not recognise is scored, and visible, rather than silently
// left out.
func TestAModuleBehindAnotherConditionIsScored(t *testing.T) {
	src := "#[cfg(feature = \"test\")]\nmod helpers {\n    pub fn f() -> i64 {\n        1\n    }\n}\n"
	funcs, _, err := ScoredFunctions(runner.Rust, "src/lib.rs", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	assertUnits(t, "src/lib.rs", funcs, []unit{{name: "f", span: Span{First: 3, Last: 5}}})
}

// Only a name decides which side of the nesting rule a function inside another
// falls on: a named one is a unit of its own and is reported to its parent, an
// anonymous one folds and leaves nothing behind.
func TestOnlyANamedNestedFunctionIsItsOwnUnit(t *testing.T) {
	for _, c := range []struct {
		name string
		src  string
		want []unit
	}{
		{
			name: "a nested function declaration",
			src:  "export function outer(): number {\n  function inner(): number {\n    return 1;\n  }\n  return inner();\n}\n",
			want: []unit{
				{name: "outer", span: Span{First: 1, Last: 6}, nested: 1},
				{name: "inner", span: Span{First: 2, Last: 4}},
			},
		},
		{
			name: "a nested generator declaration",
			src:  "export function outer(): void {\n  function* inner() {\n    yield 1;\n  }\n  inner();\n}\n",
			want: []unit{
				{name: "outer", span: Span{First: 1, Last: 6}, nested: 1},
				{name: "inner", span: Span{First: 2, Last: 4}},
			},
		},
		{
			name: "a nested named function expression",
			src:  "export function outer(): number {\n  const inner = function named(): number {\n    return 1;\n  };\n  return inner();\n}\n",
			want: []unit{
				{name: "outer", span: Span{First: 1, Last: 6}, nested: 1},
				{name: "named", span: Span{First: 2, Last: 4}},
			},
		},
		{
			name: "a callback arrow",
			src:  "export function outer(items: number[]): number[] {\n  return items.filter((n) => n > 0);\n}\n",
			want: []unit{{name: "outer", span: Span{First: 1, Last: 3}}},
		},
		{
			name: "a callback arrow bound to a local name",
			src:  "export function outer(): number {\n  const keep = (n: number) => n > 0;\n  return keep(1) ? 1 : 0;\n}\n",
			want: []unit{{name: "outer", span: Span{First: 1, Last: 4}}},
		},
		{
			name: "a nested anonymous function expression",
			src:  "export function outer(items: number[]): number[] {\n  return items.filter(function (n) {\n    return n > 0;\n  });\n}\n",
			want: []unit{{name: "outer", span: Span{First: 1, Last: 5}}},
		},
		{
			// The arrow folds, so the declaration inside it belongs to the
			// unit the arrow folded into rather than to the arrow.
			name: "a named function inside a callback arrow",
			src: "export function outer(items: number[]): number[] {\n  return items.filter((n) => {\n" +
				"    function keep(v: number): boolean {\n      return v > 0;\n    }\n    return keep(n);\n  });\n}\n",
			want: []unit{
				{name: "outer", span: Span{First: 1, Last: 8}, nested: 1},
				{name: "keep", span: Span{First: 3, Last: 5}},
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			funcs, _, err := ScoredFunctions(runner.TypeScript, "src/a.ts", []byte(c.src))
			if err != nil {
				t.Fatal(err)
			}
			assertUnits(t, "src/a.ts", funcs, c.want)
		})
	}
}

// A closure is not a function-introducing node in Rust's table at all, so its
// branches stay in the function whose span contains them with no rule of their
// own.
func TestARustClosureIsNotItsOwnUnit(t *testing.T) {
	src := "pub fn tally(items: &[i64]) -> i64 {\n    let keep = |n: &&i64| **n > 0;\n    items.iter().filter(keep).copied().sum()\n}\n"
	funcs, _, err := ScoredFunctions(runner.Rust, "src/lib.rs", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	assertUnits(t, "src/lib.rs", funcs, []unit{{name: "tally", span: Span{First: 1, Last: 4}}})
}

// A method takes the name of the type it is written on, so two methods sharing
// a name in one component are told apart.
func TestAMethodCarriesTheTypeItIsWrittenOn(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		path string
		src  string
		want string
	}{
		{runner.Rust, "src/lib.rs", "struct Gate;\nimpl Gate {\n    fn new() -> Self {\n        Gate\n    }\n}\n", "Gate::new"},
		{runner.TypeScript, "src/a.ts", "export class Gate {\n  allow(): boolean {\n    return true;\n  }\n}\n", "Gate.allow"},
		{runner.TypeScript, "src/a.ts", "export class Gate {\n  check = (): boolean => {\n    return true;\n  };\n}\n", "Gate.check"},
	} {
		funcs, _, err := ScoredFunctions(c.lang, c.path, []byte(c.src))
		if err != nil {
			t.Fatal(err)
		}
		if len(funcs) != 1 || funcs[0].Name != c.want {
			t.Errorf("%s: names = %v, want [%s]", c.path, names(funcs), c.want)
		}
	}
}

// A file the grammar could not read is ErrUnparsed *and* no functions, never
// no functions alone: the two read identically to a caller and mean opposite
// things.
func TestAFileThatDoesNotParseYieldsNoFunctionsAndAnError(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		path string
		src  string
	}{
		{runner.Rust, "src/broken.rs", "fn f( {"},
		{runner.TypeScript, "src/broken.ts", "function f( {"},
	} {
		funcs, _, err := ScoredFunctions(c.lang, c.path, []byte(c.src))
		var unparsed ErrUnparsed
		if !errors.As(err, &unparsed) {
			t.Errorf("%s: err = %v, want ErrUnparsed", c.path, err)
		}
		if len(funcs) != 0 {
			t.Errorf("%s: scored %v from a file that did not parse", c.path, names(funcs))
		}
	}
}

// A language lydite holds no tables for is an error rather than an empty
// answer, for the reason an unparsed file is.
func TestScoredFunctionsRefusesALanguageWithNoGrammar(t *testing.T) {
	_, _, err := ScoredFunctions("cobol", "a.cbl", []byte("x"))
	var none ErrNoGrammar
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want ErrNoGrammar", err)
	}
}

// A file with nothing in it is a file with nothing to score, and says so
// without an error.
func TestAnEmptyFileScoresNothing(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		path string
	}{
		{runner.Rust, "src/lib.rs"},
		{runner.TypeScript, "src/a.ts"},
		{runner.TypeScript, "src/a.tsx"},
	} {
		funcs, _, err := ScoredFunctions(c.lang, c.path, nil)
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
		}
		if len(funcs) != 0 {
			t.Errorf("%s: scored %v from an empty file", c.path, names(funcs))
		}
	}
}

// The path conventions that mark a whole file as the suite, which the coverage
// gates and the mutation engine both read through this one rule.
func TestAWholeFileOfTestCodeIsRecognisedByItsPath(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		path string
		test bool
	}{
		{runner.Rust, "tests/integration.rs", true},
		{runner.Rust, "benches/throughput.rs", true},
		{runner.Rust, "crates/core/tests/integration.rs", true},
		{runner.Rust, "src/lib.rs", false},
		{runner.TypeScript, "src/grade.test.ts", true},
		{runner.TypeScript, "src/grade.spec.ts", true},
		{runner.TypeScript, "src/__tests__/grade.ts", true},
		{runner.TypeScript, "__tests__/grade.ts", true},
		{runner.TypeScript, "src/grade.ts", false},
		// A file merely mentioning the word is not the suite.
		{runner.TypeScript, "src/testing.ts", false},
		{runner.TypeScript, "src/latest.ts", false},
		// TSX reads the TypeScript conventions, not none at all.
		{runner.TypeScript, "src/badge.test.tsx", true},
		{runner.TypeScript, "src/badge.tsx", false},
	} {
		g, ok := GrammarFor(c.lang, c.path)
		if !ok {
			t.Fatalf("no grammar for %s", c.lang)
		}
		if got := g.TestFile(c.path); got != c.test {
			t.Errorf("TestFile(%q) = %v, want %v", c.path, got, c.test)
		}
	}
}
