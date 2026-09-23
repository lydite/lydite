package mutation

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/runner"
)

// everyLine is the unrestricted set for a whole file: a fixture is generated
// from end to end, because what a golden holds is what the grammar sees rather
// than what one diff happened to touch.
func everyLine(src []byte) map[int]bool {
	return allLines(strings.Count(string(src), "\n") + 1)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- a committed fixture named by this test
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// golden is one mutant as the fixture records it: every field, under names a
// reader of the JSON can follow. Offsets and the exact replaced text and not
// only a count, because those are what a grammar bump moves and a count alone
// passes on tables that have started reading a different node.
//
// It is a conversion of Mutant rather than a copy of some of it, so a field
// added there stops this compiling until the goldens are regenerated — which
// is the only way a new field ever reaches a fixture that guards against
// silent change.
type golden struct {
	Path     string   `json:"path"`
	Line     int      `json:"line"`
	Column   int      `json:"column"`
	Operator Operator `json:"operator"`
	Offset   int      `json:"offset"`
	Length   int      `json:"length"`
	Original string   `json:"original"`
	Mutated  string   `json:"mutated"`
	Reason   string   `json:"reason,omitempty"`
}

func goldensOf(mutants []Mutant) []golden {
	out := make([]golden, 0, len(mutants))
	for _, m := range mutants {
		out = append(out, golden(m))
	}
	return out
}

// Rust, TypeScript and Python are parsed through a pre-1.0 dependency whose
// grammar tables are regenerated on a schedule, and a bump changes which
// mutants exist. This repository declares no Rust component, so its own CI cannot
// otherwise see that change and it would reach a consumer unobserved: a mutant
// that stopped being generated is a gate that quietly stopped asking, and one
// whose byte range moved is a splice into the wrong place.
//
// Regenerate with `go test ./internal/mutation -run Golden -update`, and read
// the diff before committing it: it is the record of what the bump changed.
func TestTheGoldenMutantsAreUnchanged(t *testing.T) {
	for _, c := range []struct {
		file string
		lang runner.Lang
	}{
		{"rust.rs", runner.Rust},
		{"typescript.ts", runner.TypeScript},
		{"component.tsx", runner.TypeScript},
		{"python.py", runner.Python},
	} {
		t.Run(c.file, func(t *testing.T) {
			src := fixture(t, c.file)
			mutants, unmatched, err := GenerateTreeSitter(c.lang, c.file, src, everyLine(src))
			if err != nil {
				t.Fatal(err)
			}
			if len(unmatched) > 0 {
				t.Errorf("a fixture's declaration covers no mutant: %v", unmatched)
			}
			// Every mutant applies, which is the property a recorded offset
			// exists for: a range that has moved still round-trips through
			// the golden and fails only here.
			for _, m := range mutants {
				if _, err := m.Apply(src); err != nil {
					t.Errorf("%s does not apply to the fixture it came from: %v", m, err)
				}
			}
			compareGolden(t, c.file+".golden.json", goldensOf(mutants))
		})
	}
}

func compareGolden(t *testing.T, name string, got []golden) {
	t.Helper()
	path := filepath.Join("testdata", name)
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if *update {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- a committed fixture named by this test
	if err != nil {
		t.Fatalf("%v — regenerate with `go test ./internal/mutation -run Golden -update`", err)
	}
	if string(encoded) != string(want) {
		t.Errorf("the mutant set for %s changed.\n--- got ---\n%s\n--- want ---\n%s\n"+
			"if a grammar bump caused this, read the diff and regenerate with "+
			"`go test ./internal/mutation -run Golden -update`", name, encoded, want)
	}
}

// A .tsx file read by the TypeScript tables is a sequence of syntax errors, so
// the grammar is chosen by extension rather than by the component's language.
func TestATsxFileIsParsedByTheTsxGrammar(t *testing.T) {
	src := fixture(t, "component.tsx")
	if _, _, err := GenerateTreeSitter(runner.TypeScript, "component.tsx", src, everyLine(src)); err != nil {
		t.Fatalf("the tsx fixture did not parse: %v", err)
	}
	// The same bytes under the TypeScript tables: refused, rather than
	// mutated at byte ranges derived from a misreading.
	if _, _, err := GenerateTreeSitter(runner.TypeScript, "component.ts", src, everyLine(src)); err == nil {
		t.Error("JSX parsed cleanly as TypeScript, so the two grammars are no longer distinguishable")
	}
}

// A file the grammar could not read is an error and never an empty mutant set:
// the two are indistinguishable to a caller and mean opposite things, and the
// second renders as a component whose suite killed everything.
func TestAFileThatDoesNotParseIsRefused(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		file string
		src  string
	}{
		{runner.Rust, "broken.rs", "fn f( {"},
		{runner.TypeScript, "broken.ts", "function f( {"},
	} {
		src := []byte(c.src)
		_, _, err := GenerateTreeSitter(c.lang, c.file, src, everyLine(src))
		var unparsed ErrUnparsed
		if !errorsAs(err, &unparsed) {
			t.Errorf("%s: err = %v, want ErrUnparsed", c.file, err)
		}
	}
}

// Rust puts its unit tests inside the file they test, where no path rule can
// see them. Mutating one reports an assertion nobody asserts as a survivor an
// author can answer only by declaring it equivalent, and a gate that fires on
// ordinary work is one that gets switched off.
func TestRustsInlineTestModuleIsNotMutated(t *testing.T) {
	src := fixture(t, "rust.rs")
	mutants, _, err := GenerateTreeSitter(runner.Rust, "rust.rs", src, everyLine(src))
	if err != nil {
		t.Fatal(err)
	}
	module := strings.Index(string(src), "#[cfg(test)]")
	if module < 0 {
		t.Fatal("the fixture no longer holds a test module, so this asserts nothing")
	}
	for _, m := range mutants {
		if m.Offset >= module {
			t.Errorf("%s is inside the test module", m)
		}
	}
	// The module holds a `<` and a call the operators would otherwise reach,
	// so the assertion above is not passing because there was nothing there.
	if !strings.Contains(string(src[module:]), "< 1") {
		t.Error("the fixture's test module holds nothing an operator would have mutated")
	}
}

// A module reached only through a broader condition is not recognised and is
// mutated, which is the direction the rule has to fail in: a form this does not
// know about produces survivors an author can see, where a looser match would
// silently stop mutating code that ships.
func TestOnlyACfgTestModuleIsSkipped(t *testing.T) {
	src := []byte("#[cfg(feature = \"test\")]\nmod helpers {\n    pub fn f(x: i32) -> bool { x < 1 }\n}\n")
	mutants, _, err := GenerateTreeSitter(runner.Rust, "helpers.rs", src, everyLine(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(mutants) == 0 {
		t.Error("a module behind a feature flag was skipped as though it held tests")
	}
	// And the spacing of the real attribute does not matter.
	spaced := []byte("#[ cfg( test ) ]\nmod tests {\n    fn f(x: i32) -> bool { x < 1 }\n}\n")
	mutants, _, err = GenerateTreeSitter(runner.Rust, "spaced.rs", spaced, everyLine(spaced))
	if err != nil {
		t.Fatal(err)
	}
	if len(mutants) != 0 {
		t.Errorf("%d mutant(s) from a differently spaced #[cfg(test)] module", len(mutants))
	}
}

// A test file's own code is not the code under test. Which paths are the suite
// is internal/treesitter's rule and is asserted there; what this holds is that
// the generator asks it.
func TestTheSuiteIsNotMutated(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		path string
		src  string
		test bool
	}{
		{runner.Rust, "tests/integration.rs", "fn f(x: i32) -> bool { x < 1 }\n", true},
		{runner.Rust, "benches/throughput.rs", "fn f(x: i32) -> bool { x < 1 }\n", true},
		{runner.Rust, "src/lib.rs", "fn f(x: i32) -> bool { x < 1 }\n", false},
		{runner.TypeScript, "src/grade.test.ts", "function f(x: number) { return x < 1; }\n", true},
		{runner.TypeScript, "src/__tests__/grade.ts", "function f(x: number) { return x < 1; }\n", true},
		{runner.TypeScript, "src/grade.ts", "function f(x: number) { return x < 1; }\n", false},
	} {
		src := []byte(c.src)
		mutants, _, err := GenerateTreeSitter(c.lang, c.path, src, everyLine(src))
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if (len(mutants) == 0) != c.test {
			t.Errorf("%s: %d mutant(s), want the suite skipped = %v", c.path, len(mutants), c.test)
		}
	}
}

// A substitute is picked from the literal's value and never from its spelling.
// `0.`, `0e0` and `0.0` are one number written three ways, and a rule keyed on
// the text replaces two of them with a third spelling of the same value — a
// mutant nothing can kill, reported as a survivor against correct code.
func TestASubstituteIsADifferentValueAndNotADifferentSpelling(t *testing.T) {
	for _, c := range []struct {
		kind literalKind
		from string
		want string
	}{
		{intLiteral, "0", "1"},
		{intLiteral, "7", "0"},
		{floatLiteral, "0.0", "1.0"},
		{floatLiteral, "0e0", "1.0"},
		{floatLiteral, "1.5", "0.0"},
		{stringLiteral, `""`, `"lydite"`},
		{stringLiteral, `"x"`, `""`},
		{boolLiteral, "true", "false"},
		{boolLiteral, "false", "true"},
		// A substitute keeps the spelling its own language uses: `false` in a
		// Python mutant is a name error rather than a decision reversed.
		{boolLiteral, "True", "False"},
		{boolLiteral, "False", "True"},
		// A literal whose value cannot be read is treated as non-zero, which
		// is the answer that holds for every literal anyone writes.
		{intLiteral, "1u32", "0"},
	} {
		got, ok := substituteLiteral(c.kind, c.from)
		if !ok || got != c.want {
			t.Errorf("substituteLiteral(%d, %q) = %q, %v; want %q", c.kind, c.from, got, ok, c.want)
		}
	}
}

// The line bound is shared with the Go generator, and it covers every line a
// mutant *edits* rather than only the line it is reported at: a statement
// spanning lines outside the change would otherwise be deleted in full.
func TestOnlyRequestedLinesAreMutated(t *testing.T) {
	src := fixture(t, "typescript.ts")
	all, _, err := GenerateTreeSitter(runner.TypeScript, "typescript.ts", src, everyLine(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("the fixture yielded no mutant")
	}
	one := map[int]bool{all[0].Line: true}
	some, _, err := GenerateTreeSitter(runner.TypeScript, "typescript.ts", src, one)
	if err != nil {
		t.Fatal(err)
	}
	if len(some) == 0 {
		t.Fatal("restricting to a mutant's own line produced nothing")
	}
	for _, m := range some {
		if !one[m.Line] {
			t.Errorf("%s is outside the requested lines", m)
		}
	}
	if len(some) >= len(all) {
		t.Errorf("restricting to one line kept %d of %d mutants", len(some), len(all))
	}
}

// A chained comparison is one node whose operators field holds a token per
// link, and a mutant's identity is the token it replaces rather than the node
// that carried it: each link is mutated at its own byte range.
//
// What the chain does not get is its own exclusion. A declaration on that line
// attaches to every mutant tied for the shortest replaced text, so two
// single-character bounds are covered together — sites.go's tie-break, stated
// beside the fixture's own chain, and not a property of this extraction.
func TestEachLinkOfAChainedComparisonIsItsOwnMutant(t *testing.T) {
	src := []byte("def banded(low, score, high):\n    return low < score < high\n")
	mutants, _, err := GenerateTreeSitter(runner.Python, "banded.py", src, everyLine(src))
	if err != nil {
		t.Fatal(err)
	}
	offsets := map[int]bool{}
	for _, m := range mutants {
		if m.Original != "<" {
			t.Errorf("%s replaces %q, and the chain holds only `<`", m, m.Original)
		}
		offsets[m.Offset] = true
	}
	// A boundary shift and a negation per link, at two distinct offsets.
	if len(mutants) != 4 || len(offsets) != 2 {
		t.Errorf("%d mutant(s) at %d offset(s), want 4 at 2", len(mutants), len(offsets))
	}
}

// A conjunction is swapped for the other conjunction, and for nothing else: a
// token is only ever looked up in the table written for the node type it
// belongs to, so `and` is not offered a relational bound's shift and `<` is
// not offered a conjunction's.
func TestAConjunctionIsSwappedForTheOtherAndNothingElse(t *testing.T) {
	src := []byte("def taken(a, b, c):\n    return a and b or c < 1\n")
	mutants, _, err := GenerateTreeSitter(runner.Python, "taken.py", src, everyLine(src))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, m := range mutants {
		seen[m.Original]++
		switch m.Original {
		case "and":
			if m.Mutated != "or" || m.Operator != ConditionalConnective {
				t.Errorf("%s, want `or` as a conditional-connective swap", m)
			}
		case "or":
			if m.Mutated != "and" || m.Operator != ConditionalConnective {
				t.Errorf("%s, want `and` as a conditional-connective swap", m)
			}
		case "<":
			if m.Mutated != "<=" && m.Mutated != ">=" {
				t.Errorf("%s, want the relational tables' own answers", m)
			}
		default:
			t.Errorf("%s replaces a token this expression does not hold", m)
		}
	}
	// `a and b or c` nests, so both conjunctions are reached, and each is one
	// swap where the relational token is a shift and a negation.
	if seen["and"] != 1 || seen["or"] != 1 || seen["<"] != 2 {
		t.Errorf("mutants by token = %v, want one per conjunction and two for `<`", seen)
	}
}

// A call, an assignment and an augmented assignment each reach their deletion
// even though the tree holds none of them wrapped in an `expression_statement`
// — the runtime collapses that wrapper into its one named child, so the
// removable node itself is what `module` or `block` parents directly.
func TestARemovableNodeIsReachedThroughItsCollapsedWrapper(t *testing.T) {
	src := []byte("def run(x):\n    log(x)\n    total = x\n    total += 1\n    return total\n")
	mutants, _, err := GenerateTreeSitter(runner.Python, "run.py", src, everyLine(src))
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, m := range mutants {
		if m.Operator == RemoveStatement {
			removed[m.Original] = true
		}
	}
	for _, want := range []string{"log(x)", "total = x", "total += 1"} {
		if !removed[want] {
			t.Errorf("remove-statement mutants = %v, want one deleting %q", removed, want)
		}
	}
}

// update rewrites the golden files instead of comparing against them.
var update = flag.Bool("update", false, "rewrite the golden mutant sets in testdata")

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// The error names the file and what was wrong with it, because a caller's only
// alternative is to report a component whose suite appears to have killed
// everything.
func TestAnUnparsedFileSaysWhichAndWhy(t *testing.T) {
	err := ErrUnparsed{Path: "src/lib.rs", Reason: "the tree carries a syntax error"}
	got := err.Error()
	if !strings.Contains(got, "src/lib.rs") || !strings.Contains(got, "syntax error") {
		t.Errorf("Error() = %q, want it to name the file and the reason", got)
	}
}

// A language lydite parses no source for is an error rather than an empty
// mutant set: the two are indistinguishable to a caller and mean opposite
// things.
func TestALanguageWithNoGeneratorIsRefused(t *testing.T) {
	if _, _, err := GenerateTreeSitter("cobol", "a.cbl", []byte("x"), nil); err == nil {
		t.Error("a language with no grammar produced no error")
	}
	if _, _, err := Generate("cobol", "a.cbl", []byte("x"), nil); err == nil {
		t.Error("the dispatcher accepted a language with no generator")
	}
}
