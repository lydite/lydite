package mutation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"lydite/lydite/internal/coverage"
)

// boundary shifts a relational operator by one place, which is the off-by-one
// a test most often fails to assert.
var boundary = map[token.Token]token.Token{
	token.LSS: token.LEQ,
	token.LEQ: token.LSS,
	token.GTR: token.GEQ,
	token.GEQ: token.GTR,
}

// negate inverts a test, so a branch is taken exactly when it should not be.
var negate = map[token.Token]token.Token{
	token.LSS: token.GEQ,
	token.LEQ: token.GTR,
	token.GTR: token.LEQ,
	token.GEQ: token.LSS,
	token.EQL: token.NEQ,
	token.NEQ: token.EQL,
}

// arithmetic swaps one arithmetic operator for another.
//
// Go's `+` is also string concatenation and its operands carry no types in a
// syntax tree, so a swap on a string expression produces a mutant that does
// not compile. That is an unviable mutant, which is reported as one and
// excluded from the denominator rather than scored — the accepted cost of a
// tree without types, recorded in ADR 0016.
var arithmetic = map[token.Token]token.Token{
	token.ADD: token.SUB,
	token.SUB: token.ADD,
	token.MUL: token.QUO,
	token.QUO: token.MUL,
	token.REM: token.MUL,
}

// GenerateGo produces every mutant for one Go file, restricted to the given
// 1-indexed lines.
//
// The lines are the intersection of the change and what coverage reports as
// executed, computed by the caller: a mutant outside the change is work
// nobody asked for, and one on an uncovered line cannot be killed by
// construction.
//
// Mutants are spliced at byte offsets rather than printed from a rewritten
// tree. go/printer reformats the whole file, so every mutant would differ
// from its original in places the operator never touched — which makes a
// compile error hard to attribute, and makes two mutants of one file differ
// from each other for reasons that are not the mutation.
func GenerateGo(path string, src []byte, lines map[int]bool) ([]Mutant, error) {
	// A test file's own code is not the code under test. Mutating an
	// assertion asks whether the suite notices its own tests changing,
	// which is a question with no useful answer.
	if strings.HasSuffix(path, "_test.go") {
		return nil, nil
	}
	// Generated code is not written by the author being asked to kill the
	// mutant, and regenerating it discards the assertion they would add.
	if coverage.IsGeneratedGoSource(src) {
		return nil, nil
	}

	reasons, err := Annotations(path, src)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	g := &goGen{path: path, src: src, fset: fset, lines: lines, reasons: reasons}
	ast.Inspect(file, g.visit)
	return g.out, nil
}

type goGen struct {
	path    string
	src     []byte
	fset    *token.FileSet
	lines   map[int]bool
	reasons map[int]string
	out     []Mutant
}

// visit applies every operator that fits the node. It returns true always:
// an operator matching an outer node does not stop an inner one from also
// being mutated, and a subexpression is exactly where an off-by-one hides.
func (g *goGen) visit(n ast.Node) bool {
	switch node := n.(type) {
	case *ast.BinaryExpr:
		g.binary(node)
	case *ast.ReturnStmt:
		g.returns(node)
	case *ast.ExprStmt:
		g.remove(node.Pos(), node.End())
	case *ast.IncDecStmt:
		g.remove(node.Pos(), node.End())
	}
	return true
}

// binary emits the operator swaps that apply to one binary expression.
func (g *goGen) binary(node *ast.BinaryExpr) {
	if to, ok := boundary[node.Op]; ok {
		g.emit(ConditionalBoundary, node.OpPos, node.Op.String(), to.String())
	}
	if to, ok := negate[node.Op]; ok {
		g.emit(NegateConditional, node.OpPos, node.Op.String(), to.String())
	}
	if to, ok := arithmetic[node.Op]; ok {
		g.emit(ArithmeticOperator, node.OpPos, node.Op.String(), to.String())
	}
}

// returns substitutes a returned value where a plausible substitute is
// readable off the syntax.
//
// A tree carries no types, so only self-describing results can be replaced:
// a boolean, a number, a string literal. A returned identifier or call has no
// substitute that is certain to compile, and a mutant that reliably fails to
// build teaches nothing while costing a compilation. Those are left alone,
// which is the weaker mutation ADR 0016 accepts in exchange for one engine
// across three languages.
func (g *goGen) returns(node *ast.ReturnStmt) {
	for _, r := range node.Results {
		switch v := r.(type) {
		case *ast.Ident:
			switch v.Name {
			case "true":
				g.emit(ReplaceReturn, v.Pos(), "true", "false")
			case "false":
				g.emit(ReplaceReturn, v.Pos(), "false", "true")
			}
		case *ast.BasicLit:
			if to, ok := substitute(v); ok {
				g.emit(ReplaceReturn, v.Pos(), v.Value, to)
			}
		}
	}
}

// substitute picks a different value of the same literal kind, so the mutant
// still compiles wherever the original did.
func substitute(lit *ast.BasicLit) (string, bool) {
	switch lit.Kind {
	case token.INT:
		if n, err := strconv.ParseInt(lit.Value, 0, 64); err == nil && n == 0 {
			return "1", true
		}
		return "0", true
	case token.FLOAT:
		if lit.Value == "0.0" || lit.Value == "0" {
			return "1.0", true
		}
		return "0.0", true
	case token.STRING:
		if lit.Value == `""` {
			return `"lydite"`, true
		}
		return `""`, true
	}
	return "", false
}

// remove deletes a whole statement.
//
// Only expression statements and increment/decrement statements are removed,
// and that is the operator's whole reach in Go. Deleting an assignment or a
// declaration takes the identifier's only definition with it, so the mutant
// does not compile and is reported unviable — a compilation spent to learn
// nothing about the suite. A dropped call or a dropped `i++` compiles wherever
// the original did, and is exactly the change a test that asserts nothing
// fails to notice.
func (g *goGen) remove(from, to token.Pos) {
	text := string(g.src[g.fset.Position(from).Offset:g.fset.Position(to).Offset])
	g.emitRange(RemoveStatement, from, from, to, text, "")
}

// emit records a mutant replacing the token at pos.
func (g *goGen) emit(op Operator, pos token.Pos, from, to string) {
	g.emitRange(op, pos, pos, pos+token.Pos(len(from)), from, to)
}

// emitRange records a mutant replacing the bytes of [from, to), located at
// site for the reader.
func (g *goGen) emitRange(op Operator, site, from, to token.Pos, original, mutated string) {
	at := g.fset.Position(site)
	if !g.lines[at.Line] {
		return
	}
	lo, hi := g.fset.Position(from).Offset, g.fset.Position(to).Offset
	if lo < 0 || hi > len(g.src) || lo > hi {
		return
	}

	out := make([]byte, 0, len(g.src)-(hi-lo)+len(mutated))
	out = append(out, g.src[:lo]...)
	out = append(out, mutated...)
	out = append(out, g.src[hi:]...)

	g.out = append(g.out, Mutant{
		Path:     g.path,
		Line:     at.Line,
		Column:   at.Column,
		Operator: op,
		Original: original,
		Mutated:  mutated,
		Source:   out,
		Reason:   g.reasons[at.Line],
	})
}
