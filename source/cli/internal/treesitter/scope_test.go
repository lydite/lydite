package treesitter

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/runner"
)

// declare is the token an author writes, with a reason, as one line comment.
const declare = "// [lydite:exclude_from_coverage][measured elsewhere]"

// spans is the declared spans as a set, which is what a caller compares.
func spans(t *testing.T, lang runner.Lang, path, src string) map[Span]string {
	t.Helper()
	declared, err := DeclaredExclusions(lang, path, []byte(src), annotation.Coverage)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(declared.Unused) != 0 {
		t.Fatalf("%s: declarations at %v covered nothing", path, declared.Unused)
	}
	return declared.Funcs
}

func only(t *testing.T, funcs map[Span]string) Span {
	t.Helper()
	if len(funcs) != 1 {
		t.Fatalf("%d spans, want 1: %v", len(funcs), funcs)
	}
	for span := range funcs {
		return span
	}
	return Span{}
}

// A declaration covers the whole function written below it — the signature
// line included, because a coverage report records it, and the closing brace
// included, because everything between belongs to the author's claim.
func TestADeclarationCoversTheFunctionBelowIt(t *testing.T) {
	for _, c := range []struct {
		name string
		lang runner.Lang
		path string
		src  string
		want Span
	}{
		{
			name: "a rust free function",
			lang: runner.Rust, path: "lib.rs",
			src:  "fn above() {}\n\n" + declare + "\nfn f(n: i64) -> i64 {\n    n\n}\n",
			want: Span{First: 4, Last: 6},
		},
		{
			name: "a rust method in an impl block",
			lang: runner.Rust, path: "lib.rs",
			src:  "struct C;\n\nimpl C {\n    " + declare + "\n    fn f(&self) -> i64 {\n        1\n    }\n}\n",
			want: Span{First: 5, Last: 7},
		},
		{
			name: "a rust nested fn item",
			lang: runner.Rust, path: "lib.rs",
			src:  "fn outer() {\n    " + declare + "\n    fn inner() -> i64 {\n        1\n    }\n}\n",
			want: Span{First: 3, Last: 5},
		},
		{
			// The span is the export_statement's, which begins on the same
			// line and ends on the same line as the function it wraps.
			name: "an exported typescript function",
			lang: runner.TypeScript, path: "a.ts",
			src:  declare + "\nexport function f(n: number): number {\n  return n;\n}\n",
			want: Span{First: 2, Last: 4},
		},
		{
			name: "an arrow function bound to a name",
			lang: runner.TypeScript, path: "a.ts",
			src:  declare + "\nexport const f = (n: number) => {\n  return n;\n};\n",
			want: Span{First: 2, Last: 4},
		},
		{
			name: "a class method",
			lang: runner.TypeScript, path: "a.ts",
			src:  "class C {\n  " + declare + "\n  f(): number {\n    return 1;\n  }\n}\n",
			want: Span{First: 3, Last: 5},
		},
		{
			name: "a tsx component, under its own tables",
			lang: runner.TypeScript, path: "a.tsx",
			src:  declare + "\nexport function F(): JSX.Element {\n  return <div />;\n}\n",
			want: Span{First: 2, Last: 4},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := only(t, spans(t, c.lang, c.path, c.src)); got != c.want {
				t.Errorf("span = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A reason wrapping onto the next comment line does not push the function out
// of reach: the walk skips every comment between the declaration and the thing
// it is about, whichever of them the reason used.
func TestAWrappedReasonStillReachesTheFunction(t *testing.T) {
	src := "// [lydite:exclude_from_coverage][it shells out to a foreign\n" +
		"// toolchain, so no unit test runs it]\n" +
		"fn f() -> i64 {\n    1\n}\n"
	want := Span{First: 3, Last: 5}
	funcs := spans(t, runner.Rust, "lib.rs", src)
	if got := only(t, funcs); got != want {
		t.Errorf("span = %+v, want %+v", got, want)
	}
	if got := funcs[want]; !strings.Contains(got, "foreign toolchain") {
		t.Errorf("reason = %q, want the wrapped halves joined", got)
	}
}

// An attribute and a decorator attach to the declaration below them rather
// than standing between it and its doc comment. Stopping at one would report
// every declaration on an attributed function as covering nothing.
func TestADecorationDoesNotDetachADeclaration(t *testing.T) {
	for _, c := range []struct {
		name string
		lang runner.Lang
		path string
		src  string
		want Span
	}{
		{
			name: "a rust attribute",
			lang: runner.Rust, path: "lib.rs",
			src:  declare + "\n#[inline]\nfn f() -> i64 {\n    1\n}\n",
			want: Span{First: 3, Last: 5},
		},
		{
			name: "a typescript decorator",
			lang: runner.TypeScript, path: "a.ts",
			src:  "class C {\n  " + declare + "\n  @log\n  f(): number {\n    return 1;\n  }\n}\n",
			want: Span{First: 4, Last: 6},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := only(t, spans(t, c.lang, c.path, c.src)); got != c.want {
				t.Errorf("span = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A declaration that reaches no function is named rather than dropped. Its
// author believes they have answered a finding, and nothing they can see says
// otherwise.
func TestADeclarationThatReachesNoFunctionIsNamed(t *testing.T) {
	for _, c := range []struct {
		name string
		lang runner.Lang
		path string
		src  string
		line int
	}{
		{
			// The commonest mistake: written inside the body rather than
			// above the signature, where it reads perfectly and does nothing.
			name: "inside a function body",
			lang: runner.Rust, path: "lib.rs",
			src:  "fn f() -> i64 {\n    " + declare + "\n    let n = 1;\n    n\n}\n",
			line: 2,
		},
		{
			// An impl block holds functions and introduces none of its own.
			// Covering every method in it is a widening nobody wrote.
			name: "above an impl block",
			lang: runner.Rust, path: "lib.rs",
			src:  "struct C;\n\n" + declare + "\nimpl C {\n    fn f(&self) {}\n}\n",
			line: 3,
		},
		{
			name: "above a struct",
			lang: runner.Rust, path: "lib.rs",
			src:  declare + "\nstruct C {\n    v: i64,\n}\n",
			line: 1,
		},
		{
			name: "above a typescript constant that is not a function",
			lang: runner.TypeScript, path: "a.ts",
			src:  declare + "\nexport const limit = 3;\n",
			line: 1,
		},
		{
			name: "at the end of the file, with nothing below it",
			lang: runner.TypeScript, path: "a.ts",
			src:  "export function f(): number {\n  return 1;\n}\n" + declare + "\n",
			line: 4,
		},
		{
			// A blank line between a declaration and the function it once
			// named must not let it reattach to whatever follows once that
			// function is edited away — the same bound Go's own doc-comment
			// attachment already carries.
			name: "a blank line separates it from what follows",
			lang: runner.Rust, path: "lib.rs",
			src:  declare + "\n\nfn f() -> i64 {\n    1\n}\n",
			line: 1,
		},
		{
			// A comment immediately below the declaration that is not part
			// of its own wrapped reason must stop the walk rather than
			// being skipped like a reason's own continuation line would be.
			name: "an unrelated adjacent comment separates it from what follows",
			lang: runner.Rust, path: "lib.rs",
			src:  declare + "\n// an unrelated comment\nfn f() -> i64 {\n    1\n}\n",
			line: 1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			declared, err := DeclaredExclusions(c.lang, c.path, []byte(c.src), annotation.Coverage)
			if err != nil {
				t.Fatal(err)
			}
			if len(declared.Funcs) != 0 {
				t.Errorf("covered %v, want nothing", declared.Funcs)
			}
			if len(declared.Unused) != 1 || declared.Unused[0] != c.line {
				t.Errorf("unused = %v, want [%d]", declared.Unused, c.line)
			}
		})
	}
}

// Unused is sorted, because DeclaredExclusions builds it from a map keyed by
// line and Go's own iteration order over one is randomized. Six entries make
// an already-sorted iteration order astronomically unlikely by chance, so a
// dropped sort fails this reliably rather than only sometimes.
func TestUnusedIsSortedNotIterationOrder(t *testing.T) {
	var src strings.Builder
	var want []int
	line := 1
	for i := range 6 {
		want = append(want, line)
		src.WriteString(declare)
		src.WriteString("\n")
		fmt.Fprintf(&src, "struct S%d;\n\n", i)
		line += 3
	}
	declared, err := DeclaredExclusions(runner.Rust, "lib.rs", []byte(src.String()), annotation.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(declared.Unused, want) {
		t.Errorf("Unused = %v, want %v in ascending order", declared.Unused, want)
	}
}

// A declaration for one gate answers no other, so a mutation declaration is
// invisible to a caller asking about coverage.
func TestADeclarationAnswersOnlyItsOwnGate(t *testing.T) {
	src := "// [lydite:exclude_from_mutation][nothing observes the change]\nfn f() -> i64 {\n    1\n}\n"
	declared, err := DeclaredExclusions(runner.Rust, "lib.rs", []byte(src), annotation.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if len(declared.Funcs) != 0 || len(declared.Unused) != 0 {
		t.Errorf("a mutation declaration read as coverage: %+v", declared)
	}
}

// A declaration with no reason is an error and never a silently ignored
// comment: its author has a finding they believe they have already answered.
func TestADeclarationWithNoReasonIsRefused(t *testing.T) {
	src := "// [lydite:exclude_from_coverage]\nfn f() -> i64 {\n    1\n}\n"
	_, err := DeclaredExclusions(runner.Rust, "lib.rs", []byte(src), annotation.Coverage)
	var noReason annotation.ErrNoReason
	if !errors.As(err, &noReason) {
		t.Fatalf("err = %v, want ErrNoReason", err)
	}
	if noReason.Line != 1 {
		t.Errorf("line = %d, want 1", noReason.Line)
	}
}

// A file the tables refuse is an error and never an empty answer. The two are
// indistinguishable to a caller and mean opposite things, and each caller
// decides which of them it can live with.
func TestAFileThatDoesNotParseIsRefused(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		path string
		src  string
	}{
		{runner.Rust, "broken.rs", "fn f( {"},
		{runner.TypeScript, "broken.ts", "function f( {"},
		// JSX under the TypeScript tables, which is why .tsx is its own
		// grammar rather than the same one by another extension.
		{runner.TypeScript, "component.ts", "export const F = () => <div />;\n"},
	} {
		_, err := DeclaredExclusions(c.lang, c.path, []byte(c.src), annotation.Coverage)
		var unparsed ErrUnparsed
		if !errors.As(err, &unparsed) {
			t.Errorf("%s: err = %v, want ErrUnparsed", c.path, err)
		}
	}
}

// A language lydite holds no tables for is an error rather than an empty
// answer, for the reason an unparsed file is.
func TestALanguageWithNoGrammarIsRefused(t *testing.T) {
	_, err := DeclaredExclusions("cobol", "a.cbl", []byte("x"), annotation.Coverage)
	var none ErrNoGrammar
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want ErrNoGrammar", err)
	}
	if !strings.Contains(none.Error(), "cobol") {
		t.Errorf("Error() = %q, want it to name the language", none.Error())
	}
}

// .ts and .tsx are one language to lydite and two grammars to tree-sitter, and
// every reader of a tree resolves which through this one selector.
func TestGrammarForPicksTheTablesByExtension(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		file string
		want Grammar
		ok   bool
	}{
		{runner.Rust, "src/lib.rs", Rust, true},
		{runner.TypeScript, "src/a.ts", TypeScript, true},
		{runner.TypeScript, "src/a.tsx", TSX, true},
		{runner.TypeScript, "src/a.TSX", TSX, true},
		{runner.TypeScript, "src/a.js", TypeScript, true},
		{runner.Go, "a.go", 0, false},
	} {
		got, ok := GrammarFor(c.lang, c.file)
		if ok != c.ok || got != c.want {
			t.Errorf("GrammarFor(%q, %q) = %v, %v; want %v, %v", c.lang, c.file, got, ok, c.want, c.ok)
		}
	}
}
