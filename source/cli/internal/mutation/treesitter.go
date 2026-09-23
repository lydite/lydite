package mutation

import (
	"strconv"

	"github.com/odvcencio/gotreesitter"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/treesitter"
)

// Rust, TypeScript and Python are parsed with tree-sitter, through a pure-Go
// runtime.
//
// Which parse tables read a file, and how a tree that will not parse is
// reported, are internal/treesitter's — the coverage gates ask the same two
// questions of the same three languages, and .ts and .tsx being one language
// to lydite and two grammars to tree-sitter is a rule that must not be written
// down twice. What stays here is the operator catalogue: the node types each
// of the five operators rewrites.
//
// The grammar tables are the reason the operator catalogue survives three
// languages: each operator is a rewrite of a syntax node, and the node types
// below are the whole of what lydite has to know per language. What it must
// not do is read the bytes itself — a scan that decided for itself what a
// comment or an operator is would have to lex three languages correctly to be
// right once, and every case it got wrong would either honour a declaration
// nobody made or mutate the inside of a string.
//
// A grammar bump can change which mutants exist, and this repository has no
// Rust component of its own, so its CI could not otherwise see that change
// before it reached a consumer. TestTheGoldenMutantsAreUnchanged is what makes
// it visible, in `go test`, which is what the merge gate blocks on.

// boundaryOps shifts a relational operator by one place, which is the
// off-by-one a test most often fails to assert.
var boundaryOps = map[string]string{"<": "<=", "<=": "<", ">": ">=", ">=": ">"}

// negateOps inverts a test, so a branch is taken exactly when it should not
// be. The strict forms are TypeScript's alone and are harmless in one table:
// no Rust program produces them, so a shared map cannot mistranslate one.
var negateOps = map[string]string{
	"<": ">=", "<=": ">", ">": "<=", ">=": "<",
	"==": "!=", "!=": "==", "===": "!==", "!==": "===",
}

// arithmeticOps swaps one arithmetic operator for another.
//
// `+` is also concatenation in both languages and a syntax tree carries no
// types, so a swap on a string expression produces a mutant that does not
// compile in Rust and one that produces NaN in TypeScript. The first is
// reported unviable and excluded from the denominator; the second is a real
// mutant a test either notices or does not. That is the accepted cost of a
// tree without types.
var arithmeticOps = map[string]string{"+": "-", "-": "+", "*": "/", "/": "*", "%": "*"}

// booleanOps swaps one short-circuiting conjunction for the other, which
// widens or narrows the set of inputs a branch is taken for the way a
// relational bound's own shift does. It is reported under its own operator
// kind all the same: a conjunction swap is not a relational shift, and a
// report naming it ConditionalBoundary would have a reader expecting a
// `<`-to-`<=` off-by-one at a line that holds no comparison.
//
// The table is reached only from a node type written for it. Rust's and
// TypeScript's `&&` and `||` are tokens of the same `binary_expression` a
// relational comparison is, so that node is offered the comparison tables and
// this one is Python's `boolean_operator` alone.
var booleanOps = map[string]string{"and": "or", "or": "and"}

// swap is one operator's rewrite table: which Operator the mutant is reported
// as, and the tokens it replaces.
type swap struct {
	operator Operator
	table    map[string]string
}

// comparisonSwaps are the rewrites offered to a relational, equality or
// arithmetic token.
var comparisonSwaps = []swap{
	{ConditionalBoundary, boundaryOps},
	{NegateConditional, negateOps},
	{ArithmeticOperator, arithmeticOps},
}

// booleanSwaps are the rewrites offered to a conjunction.
var booleanSwaps = []swap{{ConditionalConnective, booleanOps}}

// literalKind is what sort of value a literal node holds, which is all that is
// needed to pick a substitute of the same type.
type literalKind int

const (
	boolLiteral literalKind = iota
	intLiteral
	floatLiteral
	stringLiteral
)

// grammar is everything lydite knows about one tree-sitter language: which
// node types the catalogue's five operators apply to, and how to tell a
// comment. Which files and which modules are the suite rather than the code
// under test is internal/treesitter's, asked through tables: the coverage
// gates classify test code by the same rule, and a second copy would agree
// until one of them was edited for one gate's reason.
//
// A table rather than a function per language, because the operators are the
// same five and only the node names differ. A language that needed its own
// traversal would be evidence the catalogue had stopped being one taxonomy.
type grammar struct {
	// tables names the parse tables this catalogue's node names belong to.
	tables treesitter.Grammar
	// comment is the node type of a line comment. Only the line form can
	// carry a declaration; a block comment's text does not begin with the
	// marker, so it is refused by the text rather than by a second rule.
	comment string
	// binary are the infix expressions this language writes, each naming the
	// field the operator rewrites replace a token of.
	binary []binaryKind
	// returns are the node types that carry a returned value.
	returns []string
	// statement is the node type wrapping one expression used as a statement.
	statement string
	// removable are the inner node types a whole-statement deletion applies
	// to.
	//
	// Not every expression statement: in both grammars this node also wraps
	// `if` and `return`, and deleting one of those is a control-flow change
	// that mostly fails to compile — an unviable mutant per branch, which is
	// noise about the generator rather than evidence about the tests. A call,
	// an assignment and an increment are the statements whose absence a test
	// can actually be asked about, which is what Go's own ExprStmt and
	// IncDecStmt already restrict the operator to.
	removable map[string]bool
	// statementParents are the node types whose direct children stand for
	// statements on their own, named only where `statement`'s own wrapper
	// cannot reach a removable node: Python's runtime collapses an
	// `expression_statement` holding a single named child into that child, so
	// the removable node ends up a direct child of `module` or `block`
	// instead of wrapped. A grammar whose wrapper always survives leaves this
	// nil, and a removable node is then only ever reached through `statement`.
	statementParents []string
	// literals maps a literal's node type to what it holds.
	literals map[string]literalKind
}

// binaryKind is one infix node type, the field its operator tokens are in, and
// the rewrites those tokens are offered.
//
// A field rather than a search of the text between the operands: the field is
// the grammar's own answer about which bytes are the operator, and its node's
// range measures them exactly. Arity is part of that answer — a Python chain,
// `a < b < c`, is one node whose field holds both tokens, where every other
// infix node in the catalogue holds one.
//
// The swaps are per node type because a token's meaning is the node's: `and`
// is not a relational bound and `<` is not a conjunction, so neither is looked
// up in the other's table.
type binaryKind struct {
	nodeType string
	field    string
	multiple bool
	swaps    []swap
}

// infixKind is the rule for one node type, when the language writes an infix
// expression as that node.
func (g grammar) infixKind(nodeType string) (binaryKind, bool) {
	for _, kind := range g.binary {
		if kind.nodeType == nodeType {
			return kind, true
		}
	}
	return binaryKind{}, false
}

// rustGrammar, tsGrammar and pythonGrammar are the tables, verified against
// the grammars themselves rather than read off documentation.
var rustGrammar = grammar{
	tables:  treesitter.Rust,
	comment: "line_comment",
	binary: []binaryKind{
		{nodeType: "binary_expression", field: "operator", swaps: comparisonSwaps},
	},
	returns:   []string{"return_expression"},
	statement: "expression_statement",
	removable: map[string]bool{
		"call_expression":          true,
		"macro_invocation":         true,
		"assignment_expression":    true,
		"compound_assignment_expr": true,
	},
	literals: map[string]literalKind{
		"boolean_literal": boolLiteral,
		"integer_literal": intLiteral,
		"float_literal":   floatLiteral,
		"string_literal":  stringLiteral,
	},
}

var tsGrammar = grammar{
	tables:  treesitter.TypeScript,
	comment: "comment",
	binary: []binaryKind{
		{nodeType: "binary_expression", field: "operator", swaps: comparisonSwaps},
	},
	returns:   []string{"return_statement"},
	statement: "expression_statement",
	removable: map[string]bool{
		"call_expression":                 true,
		"assignment_expression":           true,
		"augmented_assignment_expression": true,
		"update_expression":               true,
	},
	// `true` and `false` are node types in this grammar rather than one
	// boolean node carrying its text, so both appear.
	literals: map[string]literalKind{
		"true":   boolLiteral,
		"false":  boolLiteral,
		"number": intLiteral,
		"string": stringLiteral,
	},
}

// tsxGrammar is the same catalogue read under the TSX tables: every node type
// named here is spelled identically in both grammars, and only the tables
// differ.
var tsxGrammar = grammar{
	tables:    treesitter.TSX,
	comment:   tsGrammar.comment,
	binary:    tsGrammar.binary,
	returns:   tsGrammar.returns,
	statement: tsGrammar.statement,
	removable: tsGrammar.removable,
	literals:  tsGrammar.literals,
}

// pythonGrammar is the catalogue read under the Python tables, where the infix
// expressions Rust and TypeScript each write as one node are three: arithmetic
// and bitwise in `binary_operator`, the word conjunctions in
// `boolean_operator`, and every relational and equality token in
// `comparison_operator`, whose `operators` field holds one token per link of a
// chain.
//
// `boolean_operator` nests for a chain — `a and b or c` is two nodes — so its
// `operator` field is singular like the other two, and only
// `comparison_operator` is read for several tokens at once.
//
// `expression_statement` is the wrapper a deletion applies to, but the
// runtime collapses one holding a single named child into that child — which
// is every ordinary call, assignment and augmented assignment — so the
// removable node is reached as a direct child of `module` or `block` instead,
// through `statementParents` rather than through `statement`.
var pythonGrammar = grammar{
	tables:  treesitter.Python,
	comment: "comment",
	binary: []binaryKind{
		{nodeType: "binary_operator", field: "operator", swaps: comparisonSwaps},
		{nodeType: "boolean_operator", field: "operator", swaps: booleanSwaps},
		{nodeType: "comparison_operator", field: "operators", multiple: true, swaps: comparisonSwaps},
	},
	returns:          []string{"return_statement"},
	statement:        "expression_statement",
	statementParents: []string{"module", "block"},
	removable: map[string]bool{
		"call":                 true,
		"assignment":           true,
		"augmented_assignment": true,
	},
	// `True` and `False` are node types in this grammar rather than one
	// boolean node carrying its text, so both appear, spelled the way the
	// grammar names them.
	literals: map[string]literalKind{
		"true":    boolLiteral,
		"false":   boolLiteral,
		"integer": intLiteral,
		"float":   floatLiteral,
		"string":  stringLiteral,
	},
}

// grammarFor picks the operator catalogue for one file, off the same tables
// answer every other reader of a tree-sitter tree resolves.
//
// A language lydite has no tables for yields no mutants rather than an error:
// the TypeScript family includes plain JavaScript, which the orphan gate counts
// as source, and refusing a whole component over one .mjs would be a gate
// firing on ordinary work.
func grammarFor(lang runner.Lang, file string) (grammar, bool) {
	tables, ok := treesitter.GrammarFor(lang, file)
	if !ok {
		return grammar{}, false
	}
	switch tables {
	case treesitter.Rust:
		return rustGrammar, true
	case treesitter.TypeScript:
		return tsGrammar, true
	case treesitter.TSX:
		return tsxGrammar, true
	case treesitter.Python:
		return pythonGrammar, true
	default:
		// Tables with no catalogue beside them. A grammar added below and not
		// here must yield no mutants rather than the last catalogue written.
		return grammar{}, false
	}
}

// ErrUnparsed reports a file the grammar could not read.
//
// It is an error rather than an empty mutant set, because the two mean
// opposite things and a caller cannot tell them apart: no mutants is a change
// nothing could be asked about, and an unparsed file is source lydite examined
// nothing of. Reported, it is a component that says so; swallowed, it is a
// component whose suite appears to have killed everything.
//
// One type and not a mutation-shaped copy of one, so that a caller holding
// both a mutation run and a coverage figure matches a file neither of them
// could read exactly once.
type ErrUnparsed = treesitter.ErrUnparsed

// GenerateTreeSitter produces every mutant for one Rust, TypeScript or Python
// file, restricted to the given 1-indexed lines.
//
// The same contract GenerateGo has, and the same bounds: the lines are the
// intersection of the change and what coverage reports as executed, and the
// restriction covers every line a mutant *edits* rather than only the line it
// is reported at.
func GenerateTreeSitter(lang runner.Lang, path string, src []byte, lines map[int]bool) ([]Mutant, []UnmatchedDeclaration, error) {
	if err := checkPath(path); err != nil {
		return nil, nil, err
	}
	g, ok := grammarFor(lang, path)
	if !ok {
		return nil, nil, ErrNoGenerator{Lang: lang}
	}
	if g.tables.TestFile(path) {
		return nil, nil, nil
	}
	// A file the tables refuse is not mutated at all. Mutating the parts the
	// grammar did understand would report a mutant at a byte range derived
	// from a misreading, and an author cannot tell that from a real survivor.
	root, language, err := g.tables.Parse(path, src)
	if err != nil {
		return nil, nil, err
	}

	t := &tsGen{sites: sites{path: path, src: src, lines: lines}, g: g, lang: language}
	reasons, err := annotation.Declarations(path, annotation.Mutation, t.comments(root))
	if err != nil {
		return nil, nil, err
	}
	t.reasons = annotation.Reasons(reasons)
	t.visit(root)
	return t.out, t.resolve(), nil
}

type tsGen struct {
	sites
	g    grammar
	lang *gotreesitter.Language
}

// comments reports every line comment the grammar found.
//
// The parser is the authority on what a comment is, so a token inside a string
// or inside a block comment is not one. The line number comes from the same
// tree the mutants are located from, so the two cannot drift.
func (t *tsGen) comments(root *gotreesitter.Node) []annotation.Comment {
	var out []annotation.Comment
	t.walk(root, func(n *gotreesitter.Node) {
		if n.Type(t.lang) != t.g.comment {
			return
		}
		out = append(out, annotation.Comment{Line: int(n.StartPoint().Row) + 1, Text: n.Text(t.src)})
	})
	return out
}

// walk visits every node, named and anonymous alike. Anonymous nodes are where
// the operator tokens are, and a named-only walk would find none of them.
func (t *tsGen) walk(n *gotreesitter.Node, fn func(*gotreesitter.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for i := range n.ChildCount() {
		t.walk(n.Child(i), fn)
	}
}

// visit applies every operator that fits each node, and descends into nothing
// that a module holding the suite contains.
//
// An operator matching an outer node does not stop an inner one from also
// being mutated: a subexpression is exactly where an off-by-one hides.
//
// The suite is pruned here rather than filtered out of each mutant. A node is
// inside a test module or entirely outside it — a tree cannot straddle one —
// so the subtree is the thing to leave alone, and a range comparison whose
// edges only the module itself could reach is a boundary no test could ever be
// asked about.
func (t *tsGen) visit(n *gotreesitter.Node) {
	if n == nil || t.g.tables.TestModule(n, t.src, t.lang) {
		return
	}
	typ := n.Type(t.lang)
	switch kind, infix := t.g.infixKind(typ); {
	case infix:
		t.binary(n, kind)
	case slicesContains(t.g.returns, typ):
		t.returns(n)
	case typ == t.g.statement:
		t.removeStatement(n)
	case t.g.removable[typ] && t.inStatementPosition(n):
		t.emit(RemoveStatement, n, "")
	}
	for i := range n.ChildCount() {
		t.visit(n.Child(i))
	}
}

// inStatementPosition reports whether n is a direct child of a node named in
// statementParents — the position a removable node reaches only on its own,
// for a grammar whose wrapper does not always survive to be matched by
// `statement`.
//
// Asked of the node itself rather than folded into removeStatement: the two
// paths to a removable node — wrapped, and standing for its own collapsed
// wrapper — end in the same emit, but only one of them still has a wrapper
// node to delete instead of the construct itself.
func (t *tsGen) inStatementPosition(n *gotreesitter.Node) bool {
	parent := n.Parent()
	return parent != nil && slicesContains(t.g.statementParents, parent.Type(t.lang))
}

// binary emits the operator swaps that apply to one infix expression.
//
// The operators are taken from the tree's own field rather than found in the
// text between the operands. Each token's node has a byte range measuring the
// source exactly, which is what lets the mutant carry the bytes it actually
// replaced — and what makes a chained comparison one mutant per token rather
// than one mutant over a span covering both.
func (t *tsGen) binary(n *gotreesitter.Node, kind binaryKind) {
	for _, op := range t.operators(n, kind) {
		text := op.Text(t.src)
		for _, swap := range kind.swaps {
			if to, ok := swap.table[text]; ok {
				t.emit(swap.operator, op, to)
			}
		}
	}
}

// operators is every token in one infix node's operator field, read at the
// arity the field is declared with.
//
// A singular field is one ChildByFieldName away. A repeated one is not:
// ChildByFieldName returns the first child in a field and nothing else, so a
// chain whose field holds several tokens would be mutated at its first link
// and left alone everywhere after it. The walk over FieldNameForChild is what
// reaches the rest, there being no ChildrenByFieldName in the runtime.
func (t *tsGen) operators(n *gotreesitter.Node, kind binaryKind) []*gotreesitter.Node {
	if !kind.multiple {
		if op := n.ChildByFieldName(kind.field, t.lang); op != nil {
			return []*gotreesitter.Node{op}
		}
		return nil
	}
	var out []*gotreesitter.Node
	for i := range n.ChildCount() {
		if c := n.Child(i); c != nil && n.FieldNameForChild(i, t.lang) == kind.field {
			out = append(out, c)
		}
	}
	return out
}

// returns substitutes a returned value where a plausible substitute is
// readable off the syntax.
//
// A tree carries no types, so only self-describing results can be replaced: a
// boolean, a number, a string literal. A returned identifier or call has no
// substitute that is certain to compile, and a mutant that reliably fails to
// build teaches nothing while costing a compilation.
func (t *tsGen) returns(n *gotreesitter.Node) {
	value := t.namedChild(n)
	if value == nil {
		return
	}
	kind, ok := t.g.literals[value.Type(t.lang)]
	if !ok {
		return
	}
	if to, ok := substituteLiteral(kind, value.Text(t.src)); ok {
		t.emit(ReplaceReturn, value, to)
	}
}

// removeStatement deletes a statement whose absence a test can be asked about.
func (t *tsGen) removeStatement(n *gotreesitter.Node) {
	inner := t.namedChild(n)
	if inner == nil || !t.g.removable[inner.Type(t.lang)] {
		return
	}
	t.emit(RemoveStatement, n, "")
}

// namedChild is the first named child that is part of the construct rather
// than attached to it.
//
// Comments are extra nodes in tree-sitter and may appear anywhere, including
// between `return` and its value. Taking the first named child blindly would
// read one as the returned expression, so a `return /* why */ true` would be
// left alone and a comment would be reported as the thing replaced.
func (t *tsGen) namedChild(n *gotreesitter.Node) *gotreesitter.Node {
	for i := range n.NamedChildCount() {
		c := n.NamedChild(i)
		if c != nil && !c.IsExtra() {
			return c
		}
	}
	return nil
}

// emit records a mutant replacing one node's own source range.
func (t *tsGen) emit(op Operator, n *gotreesitter.Node, mutated string) {
	start, end := n.StartPoint(), n.EndPoint()
	t.add(op,
		int(start.Row)+1, int(start.Column)+1,
		int(n.StartByte()), int(n.EndByte()),
		int(start.Row)+1, int(end.Row)+1,
		mutated)
}

// substituteLiteral picks a different value of the same kind, so the mutant
// still compiles wherever the original did.
//
// It decides on the literal's *value* and never on its spelling. `0.`, `0e0`
// and `0.0` are one number written three ways, and a rule keyed on the text
// replaces two of them with a third spelling of the same value — a mutant
// nothing can kill, which is then reported as a survivor and fails the gate on
// correct code. A literal whose value cannot be read — a Rust integer with a
// type suffix, a raw or byte string — is treated as non-zero and non-empty,
// which is the answer that holds for every literal anyone writes and is wrong
// only about a spelling of zero the parser also refuses.
func substituteLiteral(kind literalKind, text string) (string, bool) {
	switch kind {
	case boolLiteral:
		// The substitute is the other half of the pair the source is written
		// in: Python spells its booleans `True` and `False`, where Rust and
		// TypeScript spell them lower-case, and a mutant carrying the other
		// language's spelling is a name error rather than a decision reversed.
		switch text {
		case "false":
			return "true", true
		case "False":
			return "True", true
		case "True":
			return "False", true
		}
		return "false", true
	case intLiteral:
		if f, err := strconv.ParseFloat(text, 64); err == nil && f == 0 {
			return "1", true
		}
		return "0", true
	case floatLiteral:
		if f, err := strconv.ParseFloat(text, 64); err == nil && f == 0 {
			return "1.0", true
		}
		return "0.0", true
	case stringLiteral:
		if s, err := strconv.Unquote(text); err == nil && s == "" {
			return `"lydite"`, true
		}
		return `""`, true
	}
	return "", false
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
