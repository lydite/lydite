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
//
// Python needs only function_definition, for the same reasons over again: a
// method, a nested `def` and an `async def` are all that node, and `lambda` is
// its own expression type left out exactly as Rust's closure is. A decorated
// function is a function_definition inside a decorated_definition rather than
// a node of its own — see decorated.
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
	Python: {
		"function_definition": true,
	},
}

// decorations are the node types that attach to the declaration below them
// rather than standing on their own.
//
// They sit between a doc comment and the thing it is about — `#[inline]` and
// `#[derive(...)]` in Rust, `@decorator` in TypeScript and Python — and a walk
// that stopped at one would report every declaration on an attributed function
// as covering nothing. Comments need no entry: every grammar marks them extra,
// so the parser itself says they are not the next thing.
var decorations = map[Grammar]map[string]bool{
	Rust: {
		"attribute_item": true,
	},
	TypeScript: {
		"decorator": true,
	},
	Python: {
		"decorator": true,
	},
}

// decorated are the node types the walk steps *inside* rather than treating as
// the anchor or a decoration to skip past, for the one grammar that wraps a
// declaration in something other than the declaration itself.
//
// Rust and TypeScript write a decoration as a preceding sibling of the
// declaration, so the walk past them is over siblings. Python needs two
// entries, for two different reasons it wraps something around the thing a
// comment is actually about:
//
//   - `decorated_definition` — Python's `decorator` is a **child** of this
//     node, which also holds the `def` or the `class` it decorates, so the
//     walk steps into it and continues over its children, resolving to the
//     `function_definition` inside rather than to the wrapper. Anchoring on
//     the wrapper would give a declaration a span one line higher than the
//     span ScoredFunctions reports for the same function, and crap's
//     exclusion, which matches the two spans, would find no function to
//     exclude. Leaving the decorator line outside the span is what Rust
//     already does with an `#[inline]` line, no part of its function_item.
//   - `block` — the grammar lifts the *first* comment of an indented suite
//     out of the block it opens and onto the statement introducing that
//     suite, so a comment above the first of two methods in a class has the
//     whole class body — the `block` holding both methods — as its very next
//     sibling, not the first method alone. Stopping at `block` instead of
//     stepping into it would resolve the declaration's span to both methods:
//     `introducesFunction`'s own row check happens to still find the first
//     method starting on the right row from inside the block and returns
//     true, but `span(anchor)` then reports the whole block's span, wider
//     than any function ScoredFunctions ever produces on its own — the same
//     silent-widening failure an `impl` block or a decorated class is refused
//     for by name, arrived at from a different node instead of avoided.
//
// Neither wrapper is in functions for the same reason it is not the anchor:
// `decorated_definition` wraps a `class_definition` just as readily as a
// `function_definition`, and `block` is the body of every compound statement,
// not only a class's — a table entry for either would score something that is
// not a function of its own.
var decorated = map[Grammar]map[string]bool{
	Python: {
		"decorated_definition": true,
		"block":                true,
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
// the attachment is read off the tree's own sibling order here: past the
// declaration's own already-known reason lines and any decoration, bounded to
// lines with nothing blank between them, the next node is what the comment is
// about, and it is a function when it introduces one on its own first line.
//
// Taking that whole node rather than the function inside it is what makes
// `export function f()` and `@log m()` one span apiece. A declaration whose
// next sibling introduces no function — one written inside a function body,
// which reads perfectly and does nothing, or one above a struct — covers
// nothing and is reported in Unused. So does one a blank line, or a comment
// that is not its own reason's continuation, separates from what follows: see
// Grammar.scope.
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
	for line, decl := range reasons {
		span, ok := g.scope(comments, line, decl.Lines, language)
		if !ok {
			out.Unused = append(out.Unused, line)
			continue
		}
		out.Funcs[span] = decl.Reason
	}
	sort.Ints(out.Unused)
	return out, nil
}

// scope is the span a declaration covers, found from comments (every comment
// in the file, keyed by line) and the declaration's own line and how many
// further lines its reason consumed — both already known from annotation.
// Declarations, and not re-derived by a second reading of the same comments.
//
// The walk starts at the reason's own last line rather than at its first, and
// from there skips only a decoration, and only when it starts on the very
// next line. Anything else encountered there — a blank line, or a comment
// that is not this declaration's own continuation — ends the walk without a
// match. A version that skipped every comment indiscriminately once let a
// declaration reattach to whatever function happened to follow it after the
// one it was written for was edited away, with no line holding the token
// changed to trigger a referral. Bounding to adjacent lines is the same limit
// Go's own doc-comment attachment already carries — gofmt separates
// declarations with a blank line — asked of a tree instead of of go/parser.
func (g Grammar) scope(comments map[int]*gotreesitter.Node, line, lines int, language *gotreesitter.Language) (Span, bool) {
	last := comments[line+lines]
	if last == nil {
		return Span{}, false
	}
	anchor, prevEnd := g.pastDecorations(last.NextSibling(), last.EndPoint().Row, language)
	if anchor == nil || anchor.IsExtra() || anchor.StartPoint().Row != prevEnd+1 ||
		!g.introducesFunction(anchor, language, anchor.StartPoint().Row) {
		return Span{}, false
	}
	return span(anchor), true
}

// pastDecorations walks from n over the decorations written between a
// declaration's comment and the declaration itself, and answers the node the
// comment is about together with the row the last decoration ended on.
//
// Each decoration must begin on the row after the previous one ended, which is
// the adjacency bound scope describes; anything else answers nil, and the
// declaration covers nothing. A caller cannot tell an adjacency violation from
// running out of siblings without a match, and does not need to: scope treats
// a nil anchor as no match either way, so a third return carrying that
// distinction would be a bool nothing ever reads before a nil check has
// already settled the answer. Where the grammar wraps the declaration in
// something else — Python's decorated_definition, or the block a comment's
// first line inside a suite is lifted onto — the walk steps into the wrapper
// and carries on over its children, so what comes back is the declaration
// itself and the wrapper is never the anchor.
func (g Grammar) pastDecorations(n *gotreesitter.Node, prevEnd uint32,
	language *gotreesitter.Language) (*gotreesitter.Node, uint32) {
	for n != nil {
		wrapper := decorated[g.tables()][n.Type(language)]
		if !wrapper && !decorations[g.tables()][n.Type(language)] {
			return n, prevEnd
		}
		if n.StartPoint().Row != prevEnd+1 {
			return nil, prevEnd
		}
		if wrapper {
			n = n.Child(0)
			continue
		}
		prevEnd = n.EndPoint().Row
		n = n.NextSibling()
	}
	return nil, prevEnd
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
