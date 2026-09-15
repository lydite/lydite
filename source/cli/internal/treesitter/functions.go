package treesitter

import (
	"github.com/odvcencio/gotreesitter"

	"lydite/lydite/internal/runner"
)

// namedFunctions are the function-introducing node types that always carry a
// name, per grammar.
//
// It is the half of functions that can stand on its own when nested. A named
// function written inside another — Rust's nested `fn`, TypeScript's nested
// `function inner() {}` — has an identity a reader would call a function in
// its own right, so it is scored as its own unit; an arrow or an anonymous
// `function (x) {...}` passed as a callback has none, and folds into whichever
// unit's span contains it. `function_expression` and `generator_function` are
// absent because a name is optional on both: which side of the line one falls
// on is read off its own `name` field rather than off its type.
var namedFunctions = map[Grammar]map[string]bool{
	Rust: {
		"function_item": true,
	},
	TypeScript: {
		"function_declaration":           true,
		"generator_function_declaration": true,
		"method_definition":              true,
	},
}

// bindings are the node types that give an anonymous function the name it is
// written under: `const f = () => {}` is a function called `f` to everyone who
// calls it, and the tree keeps that name one level up.
var bindings = map[Grammar]map[string]string{
	Rust: {},
	TypeScript: {
		"variable_declarator":     "name",
		"public_field_definition": "name",
		"pair":                    "key",
		"assignment_expression":   "left",
	},
}

// receivers are the node types a method is written on, and the field holding
// the type's own name.
var receivers = map[Grammar]map[string]string{
	Rust: {
		"impl_item": "type",
	},
	TypeScript: {
		"class_declaration": "name",
		"class":             "name",
	},
}

// receiverSeparator is how each language writes a method's owner before its
// name.
var receiverSeparator = map[Grammar]string{
	Rust:       "::",
	TypeScript: ".",
}

// Func is one function a gate scores: the subtree a walk over it covers, the
// lines it occupies, and which of those belong to somebody else.
type Func struct {
	// Name is the function as it is written, qualified by the type a method is
	// declared on — `parse_pair`, `Gate.allow`, `C::new`. It is empty for a
	// function with no name and no binding to take one from, which is a
	// function identified by its file and line alone.
	Name string
	// Span is the extent of the whole node, the nested units' lines included.
	// Nested says which of those lines to take back out.
	Span Span
	// Node roots the walk that scores this function. It belongs to the tree
	// the enumerating call parsed, so a caller scores every function in a file
	// from one parse.
	Node *gotreesitter.Node
	// Nested is every unit directly inside this one that is scored on its own.
	//
	// Each one's lines leave this function's coverage, because no line is
	// evidence for two functions at once, and each one adds a flat 1 to this
	// function's complexity: containing a named function is a decision point
	// about this function, where inheriting the nested function's own value
	// would compound outward through arbitrary depth for no reason connected
	// to what this function does. A walk descending through Node stops at each
	// Nested[i].Node.
	Nested []Func
}

// ScoredFunctions is every function in one file a gate scores, in the order
// the tree declares them, each outer function ahead of the units nested in it.
//
// Nested units appear twice on purpose: once at the top level, because each is
// scored in its own right, and once inside the Nested of the unit containing
// them, because that unit's own score depends on them. A closure, a callback
// arrow and an anonymous `function (x) {...}` appear nowhere — they fold into
// the unit whose span already contains their lines.
//
// Test code is absent: a whole file the language's conventions mark as the
// suite yields nothing at all, and a `#[cfg(test)]` module is pruned from the
// walk. The tables the tree was read under come back with it, since every
// question about a node's type needs them and re-parsing to get them would
// give the caller a second tree whose nodes are not these.
//
// A file the grammar could not read is ErrUnparsed and never an empty answer,
// for the reason DeclaredExclusions gives: no functions is a file lydite read
// and found nothing to score in, and an unparsed file is source it examined
// nothing of.
func ScoredFunctions(lang runner.Lang, path string, src []byte) ([]Func, *gotreesitter.Language, error) {
	g, ok := GrammarFor(lang, path)
	if !ok {
		return nil, nil, ErrNoGrammar{Lang: lang}
	}
	if g.TestFile(path) {
		return nil, nil, nil
	}
	root, language, err := g.Parse(path, src)
	if err != nil {
		return nil, nil, err
	}
	w := &funcWalk{g: g, language: language, src: src}
	w.children(root, false)
	return w.out, language, nil
}

type funcWalk struct {
	g        Grammar
	language *gotreesitter.Language
	src      []byte
	// out is every unit found, in the order they were reached.
	out []Func
}

// collect walks n and answers the units directly inside it — the ones whose
// nearest enclosing unit is the one n itself sits in.
//
// inside says whether n is already within some function-like node, which is
// the whole of what decides an anonymous function's fate: written at the top
// level it is a real declaration and its own unit, written within another
// function it is a callback and folds. A folded function is transparent rather
// than terminal: a named function declared inside a callback arrow is still
// its own unit, and belongs to the unit the arrow folded into.
func (w *funcWalk) collect(n *gotreesitter.Node, inside bool) []Func {
	if n == nil || w.g.TestModule(n, w.src, w.language) {
		return nil
	}
	if !functions[w.g.tables()][n.Type(w.language)] {
		return w.children(n, inside)
	}
	if inside && !w.named(n) {
		return w.children(n, true)
	}
	f := Func{Name: w.name(n), Span: span(n), Node: n}
	at := len(w.out)
	w.out = append(w.out, f)
	f.Nested = w.children(n, true)
	w.out[at] = f
	return []Func{f}
}

// children is collect over every child, named and anonymous alike: a function
// reached only through an anonymous node is still a function.
func (w *funcWalk) children(n *gotreesitter.Node, inside bool) []Func {
	var out []Func
	for i := range n.ChildCount() {
		out = append(out, w.collect(n.Child(i), inside)...)
	}
	return out
}

// named reports whether n carries a name of its own.
func (w *funcWalk) named(n *gotreesitter.Node) bool {
	return namedFunctions[w.g.tables()][n.Type(w.language)] ||
		n.ChildByFieldName("name", w.language) != nil
}

// name is what to call the function n introduces.
//
// Its own name where it has one, the name it is bound to where it does not,
// and in either case qualified by the type it is declared on. Qualifying is
// what keeps two `new` methods in one component apart, the same reason Go's
// own scores carry a receiver.
func (w *funcWalk) name(n *gotreesitter.Node) string {
	own := w.field(n, "name")
	if own == "" {
		own = w.binding(n)
	}
	if own == "" {
		return ""
	}
	if on := w.receiver(n); on != "" {
		return on + receiverSeparator[w.g.tables()] + own
	}
	return own
}

// binding is the name an anonymous function is written under, taken from the
// declaration it is the value of.
func (w *funcWalk) binding(n *gotreesitter.Node) string {
	parent := n.Parent()
	if parent == nil {
		return ""
	}
	field, ok := bindings[w.g.tables()][parent.Type(w.language)]
	if !ok {
		return ""
	}
	return w.field(parent, field)
}

// receiver is the type a method is declared on, found by walking out to the
// nearest `impl` block or class. Empty for a free function, which is every
// function in a file with neither.
func (w *funcWalk) receiver(n *gotreesitter.Node) string {
	for at := n.Parent(); at != nil; at = at.Parent() {
		if field, ok := receivers[w.g.tables()][at.Type(w.language)]; ok {
			return w.field(at, field)
		}
	}
	return ""
}

// field is the text of one of n's fields, or empty where n has no such child.
func (w *funcWalk) field(n *gotreesitter.Node, field string) string {
	child := n.ChildByFieldName(field, w.language)
	if child == nil {
		return ""
	}
	return child.Text(w.src)
}

// span is a node's extent as 1-indexed inclusive lines, which is how every
// gate reads a coverage report.
func span(n *gotreesitter.Node) Span {
	return Span{First: int(n.StartPoint().Row) + 1, Last: int(n.EndPoint().Row) + 1}
}
