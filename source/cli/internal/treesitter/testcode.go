package treesitter

import (
	"path"
	"strings"

	"github.com/odvcencio/gotreesitter"

	"lydite/lydite/internal/runner"
)

// testCode is how one grammar's test code is told from the code it tests.
//
// It lives beside the parse tables rather than in one gate, because "this is
// the suite" is the same question whichever gate asks it: mutating an
// assertion asks whether a suite notices its own tests changing, and scoring
// one reports a complexity figure about code that ships nothing. Two copies of
// the rule would agree until one of them was edited for one gate's reason.
type testCode struct {
	// file reports whether a whole path is the suite.
	file func(path string) bool
	// module is the node type of a module whose contents are the suite, or
	// empty for a language that puts none inside the file it tests.
	//
	// Rust needs it and the others do not: `#[cfg(test)] mod tests` sits
	// inside the file under test, so no path rule can see it.
	module string
	// attribute is the attribute, whitespace removed, that marks such a
	// module. It is a preceding sibling of the module rather than a child.
	attribute string
}

var testConventions = map[Grammar]testCode{
	Rust: {
		file:      rustTestFile,
		module:    "mod_item",
		attribute: "#[cfg(test)]",
	},
	TypeScript: {
		file: typeScriptTestFile,
	},
	Python: {
		file: pythonTestFile,
	},
}

// TestFile reports whether a whole file is the suite rather than the code
// under test.
func (g Grammar) TestFile(p string) bool {
	convention, ok := testConventions[g.tables()]
	return ok && convention.file != nil && convention.file(p)
}

// TestModule reports whether n is a module whose contents are the suite.
//
// The attribute is a *preceding sibling* of the module rather than a child, so
// this reads the tree's own order rather than looking inside the module. The
// comparison is against the attribute with its whitespace removed, so
// `#[cfg( test )]` is recognised and `#[cfg(feature = "test")]` is not.
//
// A module reached only through a broader condition — `#[cfg(all(test,
// unix))]` — is not recognised, and is mutated and scored. That is the
// direction the rule has to fail in: a form this does not know about produces
// findings an author can see and answer, where a looser match would silently
// stop examining code that ships.
func (g Grammar) TestModule(n *gotreesitter.Node, src []byte, language *gotreesitter.Language) bool {
	convention := testConventions[g.tables()]
	if n == nil || convention.module == "" || convention.attribute == "" {
		return false
	}
	if n.Type(language) != convention.module {
		return false
	}
	prev := n.PrevSibling()
	return prev != nil && strings.EqualFold(compact(prev.Text(src)), convention.attribute)
}

// rustTestAttributes are the attributes under which a test carries the name
// its own function is written with.
//
// The first is the language's own and the other two are how the two async
// runtimes in wide use spell it; nextest names the function either marks
// exactly as it names a plain one. Anything else — `#[rstest]`,
// `#[wasm_bindgen_test]`, a proc-macro nobody has seen — generates names its
// own arguments decide, which is a test that exists and whose name lydite
// cannot produce.
var rustTestAttributes = map[string]bool{
	"#[test]":            true,
	"#[tokio::test]":     true,
	"#[async_std::test]": true,
}

// typeScriptSuites are the calls whose title qualifies the tests written
// inside them, and typeScriptCases the calls that declare a test of their own.
var (
	typeScriptSuites = map[string]bool{"describe": true, "suite": true}
	typeScriptCases  = map[string]bool{"it": true, "test": true}
)

// typeScriptModifiers are the properties that select how a suite or a case
// runs without changing what it is called: `it.skip("t")` is reported under
// `t`, the same as `it("t")`. `each` is deliberately absent — it takes the
// title as a format string and expands it per row, so the name in the report
// is one no reader of the source can produce.
var typeScriptModifiers = map[string]bool{
	"skip": true, "only": true, "todo": true,
	"concurrent": true, "sequential": true, "failing": true,
}

// titleSeparator is what vitest writes between an enclosing describe's title
// and the title inside it: a literal space, greater-than, space.
const titleSeparator = " > "

// callKind is what one TypeScript call declares.
type callKind int

const (
	// notATest is every call that declares no test: an assertion, a helper, a
	// `foo()()` that only looks like a chained one.
	notATest callKind = iota
	// suite is a call whose title qualifies the tests inside it.
	suite
	// testCase is a call that declares a test of its own.
	testCase
)

// DeclaredTest is one test a file declares, read off the grammar rather than
// off a run.
type DeclaredTest struct {
	// Name is the test as its runner reports it: a Rust function qualified by
	// every enclosing module with `::`, a vitest title qualified by every
	// enclosing describe with " > ". It is empty when Unreadable.
	Name string
	// Line is the 1-indexed line the declaration begins on, which is all there
	// is to identify an unreadable one by.
	Line int
	// Unreadable says a test is declared here whose name no parser can state.
	//
	// A template-literal title, a `test.each` row, a title built from a
	// variable, a Rust function behind an attribute macro that generates its
	// own names: the node says a test exists and nothing a parser reads says
	// what it will be called. It is returned rather than skipped because the
	// two answers are opposite — a caller counts this towards the tests it went
	// unable to examine, where dropping it silently reports the file as holding
	// one test fewer than it does.
	Unreadable bool
}

// DeclaredTests is every test one file declares, in the order the tree
// declares them.
//
// It answers for Rust and TypeScript the question go/ast answers for Go, and
// it answers it statically: "new" is a set difference of declared names at two
// revisions, so the enumeration has to hold whether or not anything ran.
//
// A file the grammar could not read is ErrUnparsed and never an empty answer.
// A parse failure is not a file that declares no tests, and a gate reading it
// as one goes green on the change that broke the parser. A language lydite
// parses but enumerates no tests for — Python — is ErrNoGrammar for the same
// reason, spelled out at the switch's default.
func DeclaredTests(lang runner.Lang, p string, src []byte) ([]DeclaredTest, error) {
	g, ok := GrammarFor(lang, p)
	if !ok {
		return nil, ErrNoGrammar{Lang: lang}
	}
	root, language, err := g.Parse(p, src)
	if err != nil {
		return nil, err
	}
	w := &testWalk{g: g, language: language, src: src}
	switch g.tables() {
	case Rust:
		w.rust(root, nil, g.TestFile(p))
	case TypeScript:
		w.typeScript(root, "", false)
	default:
		// A grammar with tables but no enumeration of its own is refused, not
		// answered with nothing. The two are opposite answers a caller cannot
		// tell apart, and the wrong one is silently green: "new" is a set
		// difference of declared names, so a language reported as declaring no
		// test at either revision has no new test at either, and the gate over
		// it passes without having looked.
		return nil, ErrNoGrammar{Lang: lang}
	}
	return w.out, nil
}

// testWalk carries one file's parse through the enumeration.
type testWalk struct {
	g        Grammar
	language *gotreesitter.Language
	src      []byte
	out      []DeclaredTest
}

// rust enumerates the tests under n, qualified by mods — every enclosing
// module's name, outermost first.
//
// inTest says whether n already sits in code the conventions call the suite: a
// `tests/` file, or a `#[cfg(test)]` module. It is what decides an
// unrecognised attribute's meaning, since `#[inline]` on shipped code says
// nothing about tests and `#[rstest]` inside a test module is a test whose
// names come from the macro.
func (w *testWalk) rust(n *gotreesitter.Node, mods []string, inTest bool) {
	for i := range n.ChildCount() {
		c := n.Child(i)
		switch c.Type(w.language) {
		case "mod_item":
			name := w.text(c.ChildByFieldName("name", w.language))
			inner := mods
			if name != "" {
				inner = append(mods[:len(mods):len(mods)], name)
			}
			w.rust(c, inner, inTest || w.g.TestModule(c, w.src, w.language))
		case "function_item":
			w.rustFunction(c, mods, inTest)
		default:
			w.rust(c, mods, inTest)
		}
	}
}

// rustFunction records what the attributes stacked above n say about it.
//
// A function is not descended into: a `fn` written inside a test is a helper
// that no runner names, whatever it is attributed with.
func (w *testWalk) rustFunction(n *gotreesitter.Node, mods []string, inTest bool) {
	recognised, attributed := w.rustAttributes(n)
	switch {
	case recognised:
		name := w.text(n.ChildByFieldName("name", w.language))
		w.out = append(w.out, DeclaredTest{Name: qualify(mods, name, "::"), Line: line(n)})
	case attributed && inTest:
		w.out = append(w.out, DeclaredTest{Line: line(n), Unreadable: true})
	}
}

// rustAttributes reads the whole run of attributes immediately above n, and
// answers whether any is a recognised test attribute and whether there was any
// at all.
//
// The run and not the nearest one: `#[test]` followed by `#[ignore]` leaves
// `#[ignore]` as the function's immediate preceding sibling, and a check that
// looked there alone would read a test carrying two attributes as no test.
// Comments are stepped over — both grammars mark them extra, so the parser
// itself says they are not the next thing.
func (w *testWalk) rustAttributes(n *gotreesitter.Node) (recognised, attributed bool) {
	for p := n.PrevSibling(); p != nil; p = p.PrevSibling() {
		if p.IsExtra() {
			continue
		}
		if p.Type(w.language) != "attribute_item" {
			return recognised, attributed
		}
		attributed = true
		if rustTestAttributes[strings.ToLower(attributePath(compact(p.Text(w.src))))] {
			recognised = true
		}
	}
	return recognised, attributed
}

// attributePath is an attribute's path with any argument list dropped:
// `#[tokio::test(flavor="multi_thread")]` names the same attribute as
// `#[tokio::test]` — the arguments configure the runtime, not which attribute
// it is — so a comparison against rustTestAttributes must not see them.
func attributePath(compact string) string {
	if i := strings.IndexByte(compact, '('); i != -1 && strings.HasSuffix(compact, ")]") {
		return compact[:i] + "]"
	}
	return compact
}

// typeScript enumerates the tests under n, prefixed by every enclosing
// describe's title.
//
// unreadable says one of those enclosing titles could not be read, which the
// tests inside inherit: a test under a describe nobody can name has no name of
// its own either, however plain its own title is.
func (w *testWalk) typeScript(n *gotreesitter.Node, prefix string, unreadable bool) {
	for i := range n.ChildCount() {
		c := n.Child(i)
		if c.Type(w.language) != "call_expression" {
			w.typeScript(c, prefix, unreadable)
			continue
		}
		kind, title, readable := w.typeScriptCall(c)
		switch kind {
		case notATest:
			w.typeScript(c, prefix, unreadable)
		case suite:
			w.typeScript(c, qualify([]string{prefix}, title, titleSeparator), unreadable || !readable)
		case testCase:
			if unreadable || !readable {
				w.out = append(w.out, DeclaredTest{Line: line(c), Unreadable: true})
			} else {
				w.out = append(w.out, DeclaredTest{Name: qualify([]string{prefix}, title, titleSeparator), Line: line(c)})
			}
			w.typeScript(c, prefix, unreadable)
		}
	}
}

// typeScriptCall sorts one call into a suite, a test case, or neither, and
// reads the title it was given.
//
// readable is false for a call that declares something whose title only exists
// once the file has run — a template literal, a title held in a variable, or
// any `.each(...)` chain, whose callee is itself a call and whose title is a
// format string expanded per row.
func (w *testWalk) typeScriptCall(n *gotreesitter.Node) (kind callKind, title string, readable bool) {
	callee := n.ChildByFieldName("function", w.language)
	if callee == nil {
		return notATest, "", false
	}
	base, chained := w.typeScriptCallee(callee)
	switch {
	case typeScriptSuites[base]:
		kind = suite
	case typeScriptCases[base]:
		kind = testCase
	default:
		return notATest,
			"", // [lydite:exclude_from_mutation][the caller's switch branches on kind alone for notATest, so no string spliced in here is ever read]
			false // [lydite:exclude_from_mutation][and never reads readable either, so no bool spliced in here is ever read]
	}
	if chained {
		return kind, "", false // [lydite:exclude_from_mutation][readable=false here always sends the caller past title — a chained suite marks every descendant unreadable regardless of the prefix it built, and a chained case takes the branch that does not read title at all]
	}
	args := n.ChildByFieldName("arguments", w.language)
	if args == nil || args.NamedChildCount() == 0 {
		return kind, "", false
	}
	title, readable = w.stringLiteral(args.NamedChild(0))
	return kind, title, readable
}

// typeScriptCallee names the function a call is made on, and says whether it
// was reached through a further call: `it`, `it.skip`, `it.concurrent.skip`
// and `it.each([...])` are all `it`, and only a chain ending in `.each(...)`
// is chained.
func (w *testWalk) typeScriptCallee(n *gotreesitter.Node) (base string, chained bool) {
	switch n.Type(w.language) {
	case "identifier":
		return n.Text(w.src), false
	case "member_expression":
		base, ok := w.baseIdentifierThroughModifiers(n)
		if !ok {
			return "", // [lydite:exclude_from_mutation][the caller looks this base up in typeScriptSuites and typeScriptCases, which "" fails the same way any other unrecognised name would]
				false // [lydite:exclude_from_mutation][chained is never read once that lookup fails — the default case returns before it reaches the "if chained" check]
		}
		return base, false
	case "call_expression":
		// Any modifier that returns a function before the title and body are
		// given — `.each([...])`, `.skipIf(cond)`, `.runIf(cond)` and
		// whichever of these vitest adds next — is chained. The property name
		// is not checked against a fixed list: what marks a call as one of
		// these is the shape (a call on a call on a recognised base), not
		// which word it spells, so a modifier this table has never seen is
		// still recognised as one rather than read as not a test at all.
		inner := n.ChildByFieldName("function", w.language)
		if inner == nil || inner.Type(w.language) != "member_expression" {
			return "", false
		}
		object := inner.ChildByFieldName("object", w.language)
		if object == nil {
			return "", false
		}
		base, ok := w.baseIdentifierThroughModifiers(object)
		if !ok {
			return "", false
		}
		return base, true
	default:
		return "", false
	}
}

// baseIdentifierThroughModifiers walks a chain of member expressions whose
// properties are every one a recognised modifier, down to the identifier they
// are all written on — so `it.concurrent.skip` reaches `it` exactly as
// `it.skip` does, and a chain carrying anything else (a typo, an unrecognised
// property) reaches nothing rather than the wrong base.
func (w *testWalk) baseIdentifierThroughModifiers(n *gotreesitter.Node) (string, bool) {
	switch n.Type(w.language) {
	case "identifier":
		return n.Text(w.src), true
	case "member_expression":
		object := n.ChildByFieldName("object", w.language)
		property := w.text(n.ChildByFieldName("property", w.language))
		if object == nil || !typeScriptModifiers[property] {
			return "", // [lydite:exclude_from_mutation][every caller destructures (base, ok) and reads base only when ok is true, so no string spliced in here alongside ok=false is ever read]
				false // [lydite:exclude_from_mutation][and an empty base already fails typeScriptSuites/typeScriptCases the same way any unrecognised name would, whatever ok claims — see typeScriptCallee's member_expression case]
		}
		return w.baseIdentifierThroughModifiers(object)
	default:
		return "", false
	}
}

// stringLiteral reads a title written as a plain quoted string.
//
// A string carrying an escape sequence is reported unreadable rather than
// handed back with its backslashes: what the runner reports is the escape
// decoded, and a name spelled the way the source spells it would give the
// rerun a filter matching nothing and report the test as skipped.
func (w *testWalk) stringLiteral(n *gotreesitter.Node) (string, bool) {
	if n == nil || n.Type(w.language) != "string" {
		return "", false // [lydite:exclude_from_mutation][readable=false here sends every caller past title — typeScript's switch never reads a title beside a false readable, so the empty string is never compared or stored]
	}
	text := n.Text(w.src)
	if len(text) < 2 || text[0] != text[len(text)-1] {
		return "", false
	}
	inner := text[1 : len(text)-1]
	if strings.Contains(inner, `\`) {
		return "", false // [lydite:exclude_from_mutation][the same guard as above: readable=false is what a caller acts on, and no caller reads a title beside it]
	}
	return inner, true
}

// text is a node's source, or empty where there is no node.
func (w *testWalk) text(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}
	return n.Text(w.src)
}

// qualify joins the scopes a test is written in to its own name. An empty
// scope contributes nothing, so a test at the top level is its bare name.
func qualify(scopes []string, name, separator string) string {
	parts := make([]string, 0, len(scopes)+1)
	for _, scope := range scopes {
		if scope != "" {
			parts = append(parts, scope)
		}
	}
	return strings.Join(append(parts, name), separator)
}

// line is the 1-indexed line a node begins on, which is how every gate reads a
// declaration's position.
func line(n *gotreesitter.Node) int {
	return int(n.StartPoint().Row) + 1
}

// rustTestFile reports whether a whole file is Rust's test code: the
// integration and benchmark directories the toolchain treats as test targets.
//
// The unit tests Rust puts *inside* the file they test are excluded by
// TestModule instead, since no path rule can see them.
func rustTestFile(p string) bool {
	for _, dir := range []string{"tests/", "benches/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	return false
}

// typeScriptTestFile recognises the two conventions every JavaScript runner
// lydite ships supports: a `.test.` or `.spec.` infix, and a `__tests__`
// directory.
func typeScriptTestFile(p string) bool {
	base := path.Base(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
		return true
	}
	return strings.HasPrefix(p, "__tests__/") || strings.Contains(p, "/__tests__/")
}

// pythonTestFile recognises the conventions pytest discovers a test file by: a
// `test_` prefix or a `_test` suffix on the stem, and a `tests` directory.
//
// There is no module rule beside it, because Python has no in-file convention
// for test code the way Rust's `#[cfg(test)] mod tests` is one — a test lives
// in a file pytest collects, and nothing marks one inside a file that ships.
// `conftest.py` is deliberately unrecognised: it holds fixtures and hooks
// rather than tests, and an unrecognised form failing open — scored, and so
// visible — is the direction every rule here fails in.
func pythonTestFile(p string) bool {
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test") {
		return true
	}
	return strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/")
}

// compact removes every space from an attribute, so one form is recognised
// however it is spaced.
func compact(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}
