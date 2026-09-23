package treesitter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/runner"
)

// declared enumerates one file of a materialised probe tree. The path handed
// to the enumerator is relative to the probe's own root, because that is what
// the test-code conventions read.
func declared(t *testing.T, lang runner.Lang, tree, rel string) []DeclaredTest {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	tests, err := DeclaredTests(lang, rel, src)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return tests
}

func assertDeclared(t *testing.T, rel string, got, want []DeclaredTest) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d test(s), want %d: %+v", rel, len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("%s: test %d is %+v, want %+v", rel, i, got[i], w)
		}
	}
}

// The names cargo-nextest reports for this probe, cross-checked against
// internal/junit/testdata/nextest-suite.xml: a unit test is qualified by every
// module it is written in, `#[cfg(test)] mod tests` included, and an
// integration test by the modules inside its own file alone.
func TestTheNextestProbeDeclaresTheNamesNextestReports(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "flaky", "testdata", "nextestprobe"))
	assertDeclared(t, "src/lib.rs", declared(t, runner.Rust, tree, "src/lib.rs"), []DeclaredTest{
		{Name: "tests::doubles", Line: 10},
		{Name: "tests::nested::doubles_deeper", Line: 18},
	})
	assertDeclared(t, "tests/a.rs", declared(t, runner.Rust, tree, "tests/a.rs"), []DeclaredTest{
		{Name: "shared_name", Line: 2},
		{Name: "inner::only_in_a", Line: 8},
	})
	assertDeclared(t, "tests/b.rs", declared(t, runner.Rust, tree, "tests/b.rs"), []DeclaredTest{
		{Name: "shared_name", Line: 2},
	})
}

// `#[test] #[ignore] fn` leaves `#[ignore]` as the function's immediate
// preceding sibling, so a check of that one sibling alone reads a test as no
// test. The whole run of attributes above the function is read.
//
// It is enumerated whether or not it runs: an ignored test is declared, and
// what became of it is the report's answer rather than the parser's.
func TestATestBehindASecondAttributeIsStillDeclared(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "flaky", "testdata", "nextestprobe"))
	assertDeclared(t, "tests/c.rs", declared(t, runner.Rust, tree, "tests/c.rs"), []DeclaredTest{
		{Name: "async_cases::awaits_and_agrees", Line: 3},
		{Name: "ignored_by_attribute", Line: 10},
	})
}

// An attribute macro lydite does not recognise names the tests it generates
// itself, so a function carrying one inside test code is declared with its
// name unreadable rather than assumed inert. A function with no attribute at
// all is a helper no runner names, and an attribute on shipped code says
// nothing about tests. A recognised attribute carrying its own arguments —
// `#[tokio::test(flavor = "multi_thread")]` — is still recognised: the
// arguments configure the runtime, not which attribute it is.
func TestAnUnrecognisedRustAttributeIsDeclaredUnreadable(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("testdata", "attributeprobe"))
	assertDeclared(t, "src/lib.rs", declared(t, runner.Rust, tree, "src/lib.rs"), []DeclaredTest{
		{Name: "tests::doubles", Line: 12},
		{Line: 18, Unreadable: true},
		{Name: "tests::doubles_on_multi_thread", Line: 23},
	})
}

// The titles vitest reports for this probe, cross-checked against
// internal/junit/testdata/vitest-probe-suite.xml: every enclosing describe's
// title joined to the test's own with " > ", and a title containing regex
// metacharacters read as the text it is.
func TestTheVitestProbeDeclaresTheTitlesVitestReports(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "flaky", "testdata", "vitestprobe"))
	assertDeclared(t, "src/one.test.ts", declared(t, runner.TypeScript, tree, "src/one.test.ts"), []DeclaredTest{
		{Name: "outer > inner > holds a title two describes deep", Line: 5},
		{Name: "outer > holds a title one describe deep", Line: 10},
		{Name: "shared title", Line: 15},
		{Name: "matches ^a (b) [c] + d$", Line: 19},
		{Line: 24, Unreadable: true},
		{Line: 28, Unreadable: true},
	})
	assertDeclared(t, "src/two.test.ts", declared(t, runner.TypeScript, tree, "src/two.test.ts"), []DeclaredTest{
		{Name: "shared title", Line: 3},
	})
}

// A template literal and a `test.each` row are named only once the file has
// run. Each is declared at its line with no name, never skipped and never
// guessed at: a guess gives the rerun a filter matching nothing, and a skip
// gives a green row for a test nothing looked at.
func TestATitleOnlyARunCanProduceIsDeclaredUnreadable(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("..", "flaky", "testdata", "vitestprobe"))
	for _, test := range declared(t, runner.TypeScript, tree, "src/one.test.ts") {
		if (test.Line == 24 || test.Line == 28) && (!test.Unreadable || test.Name != "") {
			t.Errorf("line %d declared %+v, want a test with no readable name", test.Line, test)
		}
	}
}

// `.skip` and `.only` select how a test runs without changing what it is
// called, however many of them are stacked; `.each` expands its title per
// row, and takes the tests written inside it with it — whether it is reached
// directly or through another modifier first. Any other modifier that returns
// a function before the title, such as `.skipIf(cond)`, is recognised as one
// by its shape and not by its name, and loses its title the same way `.each`
// does.
//
// A call this table does not recognise is not a test itself, but the walk
// still descends into it: a test declared inside a plain wrapper function is
// found exactly as one declared inside a describe is. A test nested inside
// another test's own callback is found the same way — an unusual shape, but
// the walk does not stop descending just because it is already inside one
// call it recognised. An empty title is a title: the shortest string literal
// there is, and still readable.
func TestATypeScriptModifierKeepsTheTitleAndEachLosesIt(t *testing.T) {
	tree := fixture.Tree(t, filepath.Join("testdata", "attributeprobe"))
	rel := "src/modifiers.test.ts"
	assertDeclared(t, rel, declared(t, runner.TypeScript, tree, rel), []DeclaredTest{
		{Name: "selected > is named the same as an unskipped one", Line: 4},
		{Line: 10, Unreadable: true},
		{Line: 15, Unreadable: true},
		{Name: "two stacked modifiers still name it", Line: 19},
		{Line: 23, Unreadable: true},
		{Line: 27, Unreadable: true},
		{Name: "nested inside a call this table does not recognise", Line: 32},
		{Name: "outer test", Line: 37},
		{Name: "nested inside another test's callback", Line: 38},
		{Name: "", Line: 43},
	})
}

// A file the grammar could not read is an error and never an empty answer: a
// parse failure is not a file declaring no tests, and a gate reading it as one
// goes green on the change that broke the parser.
func TestAFileTheGrammarCannotReadIsAnError(t *testing.T) {
	for _, c := range []struct {
		lang runner.Lang
		rel  string
		src  string
	}{
		{runner.Rust, "tests/broken.rs", "#[test]\nfn unbalanced( {\n"},
		{runner.TypeScript, "src/broken.test.ts", "it(\"unbalanced\", () => {\n"},
	} {
		got, err := DeclaredTests(c.lang, c.rel, []byte(c.src))
		var unparsed ErrUnparsed
		if !errors.As(err, &unparsed) {
			t.Errorf("%s: %d test(s) and error %v, want ErrUnparsed", c.rel, len(got), err)
		}
	}
}

// A language lydite holds no tree-sitter tables for is named rather than
// reported as a file declaring nothing.
func TestDeclaredTestsRefusesALanguageWithNoGrammar(t *testing.T) {
	_, err := DeclaredTests(runner.Go, "pkg/thing_test.go", []byte("package pkg\n"))
	var noGrammar ErrNoGrammar
	if !errors.As(err, &noGrammar) {
		t.Fatalf("Go came back with %v, want ErrNoGrammar", err)
	}
}

// A language whose files this package parses and whose tests it cannot
// enumerate is named too. Python gained tables for the gates that need spans
// and no test-declaration walk, and an empty slice there would read as a file
// declaring no tests — a new-test gate that goes green over every Python file
// in a repository, on the day somebody lists the language and never again.
func TestDeclaredTestsRefusesAGrammarWithNoEnumeration(t *testing.T) {
	_, err := DeclaredTests(runner.Python, "tests/test_thing.py",
		[]byte("def test_thing():\n    assert True\n"))
	var none ErrNoTestEnumeration
	if !errors.As(err, &none) {
		t.Fatalf("Python came back with %v, want ErrNoTestEnumeration", err)
	}
	if !strings.Contains(none.Error(), "python") {
		t.Errorf("Error() = %q, want it to name the language", none.Error())
	}
}

// Python's suite is told from the code it tests by pytest's own two default
// discovery names and by the directory a project separates its tests into.
// Without this, TestFile answers false for every one of them and a Python
// suite is scored and mutated as though it shipped.
func TestPythonTestFilesAreRecognisedByPytestsOwnConventions(t *testing.T) {
	for _, p := range []string{
		"tests/helpers.py",
		"src/tests/helpers.py",
		"src/test_probe.py",
		"src/probe_test.py",
		"test_probe.py",
		"probe_test.py",
	} {
		if !Python.TestFile(p) {
			t.Errorf("TestFile(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"src/probe.py",
		"src/conftest.py",
		// `latest.py` ends in `test` and is not one: the suffix pytest
		// collects is `_test`, and matching the bare word would stop scoring
		// an ordinary module.
		"src/latest.py",
		// A directory named for something else that merely begins the same
		// way is not the suite either.
		"testing/probe.py",
		"src/attestations/probe.py",
	} {
		if Python.TestFile(p) {
			t.Errorf("TestFile(%q) = true, want false", p)
		}
	}
}
