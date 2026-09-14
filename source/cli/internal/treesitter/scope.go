package treesitter

import (
	"fmt"
	"sort"

	"github.com/odvcencio/gotreesitter"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/runner"
)

// functions are the node types that introduce a function body, per grammar.
//
// Read off the trees the real grammars produce over the probe sources in
// internal/coverage/testdata, and not off documentation: `export function f()`
// is an export_statement wrapping a function_declaration, `const f = () => {}`
// is a lexical_declaration wrapping an arrow_function, and a class method is a
// method_definition whichever accessor keyword opens it.
//
// Rust needs only function_item — a method in an impl block, a nested item and
// an `async fn` are all that node. A closure is its own expression type and is
// deliberately absent: crap.md puts a closure's lines in the function that
// declares it, so the enclosing function_item is the scope either way.
var functions = map[Grammar]map[string]bool{
	Rust: {
		"function_item": true,
	},
	TypeScript: {
		"function_declaration":           true,
		"generator_function_declaration": true,
		"function_expression":            true,
		"generator_function":             true,
		"arrow_function":                 true,
		"method_definition":              true,
	},
}

// decorations are the node types that attach to the declaration below them
// rather than standing on their own.
//
// They sit between a doc comment and the thing it is about — `#[inline]` and
// `#[derive(...)]` in Rust, `@decorator` in TypeScript — and a walk that
// stopped at one would report every declaration on an attributed function as
// covering nothing. Comments need no entry: both grammars mark them extra, so
// the parser itself says they are not the next thing.
var decorations = map[Grammar]map[string]bool{
	Rust: {
		"attribute_item": true,
	},
	TypeScript: {
		"decorator": true,
	},
}

// tables is the Grammar whose node names this one shares. TSX is the
// TypeScript grammar plus JSX, so every node type named here is the same in
// both, and a second copy would agree until one of them learned something.
func (g Grammar) tables() Grammar {
	if g == TSX {
		return TypeScript
	}
	return g
}

// Span is a function's extent in a file: 1-indexed lines, both ends included.
type Span struct {
	First, Last int
}

// Declared is what one file declares about a gate: the span of each function
// whose findings their author says are not evidence, and the declarations that
// name no function at all.
type Declared struct {
	// Funcs maps each excluded function's span to the reason given.
	Funcs map[Span]string
	// Unused is the line of every declaration that covers no function, named
	// rather than dropped for the reason coverage.Excluded names its own: its
	// author believes they have answered a finding, and nothing they can see
	// says otherwise.
	Unused []int
}

// Lines is every line of every excluded function.
//
// The whole declaration and not only its body: a coverage report records the
// signature line — cargo-llvm-cov emits a DA record for the `fn` line of every
// function — so a span that started below it would leave that line in the
// figure its author has just said is not evidence.
func (d Declared) Lines() map[int]bool {
	out := map[int]bool{}
	for span := range d.Funcs {
		for line := span.First; line <= span.Last; line++ {
			out[line] = true
		}
	}
	return out
}

// DeclaredExclusions reads one file's declarations for gate and resolves each
// one to the function it covers.
//
// A declaration covers the function written immediately below it, and nothing
// else. That is the same rule go/ast applies in Go, where a doc comment is
// attached to the declaration it precedes — tree-sitter attaches nothing, so
// the attachment is read off the tree's own sibling order here: the next node
// that is neither a comment nor a decoration is what the comment is about, and
// it is a function when it introduces one on its own first line.
//
// Taking that whole node rather than the function inside it is what makes
// `export function f()` and `@log m()` one span apiece. A declaration whose
// next sibling introduces no function — one written inside a function body,
// which reads perfectly and does nothing, or one above a struct — covers
// nothing and is reported in Unused.
func DeclaredExclusions(lang runner.Lang, path string, src []byte, gate annotation.Gate) (Declared, error) {
	g, ok := GrammarFor(lang, path)
	if !ok {
		return Declared{}, ErrNoGrammar{Lang: lang}
	}
	root, language, err := g.Parse(path, src)
	if err != nil {
		return Declared{}, err
	}
	comments := commentNodes(root, language)
	reasons, err := annotation.Declarations(path, gate, commentTexts(comments, src))
	if err != nil {
		return Declared{}, fmt.Errorf("reading %s: %w", path, err)
	}
	out := Declared{Funcs: map[Span]string{}}
	for line, reason := range reasons {
		span, ok := g.scope(comments[line], language)
		if !ok {
			out.Unused = append(out.Unused, line)
			continue
		}
		out.Funcs[span] = reason
	}
	sort.Ints(out.Unused)
	return out, nil
}

// scope is the span a declaration written in comment covers.
func (g Grammar) scope(comment *gotreesitter.Node, language *gotreesitter.Language) (Span, bool) {
	if comment == nil {
		return Span{}, false
	}
	anchor := comment.NextSibling()
	for anchor != nil && (anchor.IsExtra() || decorations[g.tables()][anchor.Type(language)]) {
		anchor = anchor.NextSibling()
	}
	if anchor == nil || !g.introducesFunction(anchor, language, anchor.StartPoint().Row) {
		return Span{}, false
	}
	return Span{First: int(anchor.StartPoint().Row) + 1, Last: int(anchor.EndPoint().Row) + 1}, true
}

// introducesFunction reports whether n opens a function on row.
//
// Bounded to nodes starting on that row, which is what keeps the answer about
// the thing the declaration is written above. An `impl` block holds functions
// and introduces none of its own, so a declaration above one covers nothing
// rather than silently covering every method in it.
func (g Grammar) introducesFunction(n *gotreesitter.Node, language *gotreesitter.Language, row uint32) bool {
	if n == nil || n.StartPoint().Row != row {
		return false
	}
	if functions[g.tables()][n.Type(language)] {
		return true
	}
	for i := range n.ChildCount() {
		if g.introducesFunction(n.Child(i), language, row) {
			return true
		}
	}
	return false
}

// commentNodes is every comment in a file, keyed by the line it starts on.
//
// The parser is the authority on what a comment is, so a token inside a string
// or inside a block comment is not one. Both grammars mark a comment extra,
// which is also what lets the walk to a declaration's anchor skip them without
// naming a node type per language.
func commentNodes(root *gotreesitter.Node, language *gotreesitter.Language) map[int]*gotreesitter.Node {
	out := map[int]*gotreesitter.Node{}
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil {
			return
		}
		if n.IsExtra() && n.IsNamed() {
			line := int(n.StartPoint().Row) + 1
			// The outermost node on a line wins. A Rust doc comment holds a
			// doc_comment child at the same position, and the child has no
			// sibling order to walk from.
			if _, seen := out[line]; !seen {
				out[line] = n
			}
			return
		}
		for i := range n.ChildCount() {
			walk(n.Child(i))
		}
	}
	walk(root)
	return out
}

// commentTexts is the comments as internal/annotation reads them, in line
// order — that package reads a wrapped reason out of the lines immediately
// following the one the token is on, so the order is part of the input.
func commentTexts(comments map[int]*gotreesitter.Node, src []byte) []annotation.Comment {
	lines := make([]int, 0, len(comments))
	for line := range comments {
		lines = append(lines, line)
	}
	sort.Ints(lines)
	out := make([]annotation.Comment, 0, len(lines))
	for _, line := range lines {
		out = append(out, annotation.Comment{Line: line, Text: comments[line].Text(src)})
	}
	return out
}
