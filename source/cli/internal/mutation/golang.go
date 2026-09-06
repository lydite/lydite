package mutation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"lydite/lydite/internal/annotation"
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
// tree without types, which ADR 0016 records.
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
// construction. The restriction covers every line a mutant *edits*, not only
// the line it is reported at, so a statement spanning lines outside the set is
// left alone rather than deleted in full.
//
// Each mutant records the byte range it replaces rather than a rewritten copy
// of the file. Ranges come from the syntax nodes themselves, never from the
// length of a token's text: a raw string literal's value has its carriage
// returns stripped, so a range measured from that text is short by one byte
// per line and splices a mutant that cannot parse.
func GenerateGo(path string, src []byte, lines map[int]bool) ([]Mutant, []UnmatchedDeclaration, error) {
	if err := checkPath(path); err != nil {
		return nil, nil, err
	}
	// A test file's own code is not the code under test. Mutating an
	// assertion asks whether the suite notices its own tests changing,
	// which is a question with no useful answer.
	if strings.HasSuffix(path, "_test.go") {
		return nil, nil, nil
	}
	// Generated code is not written by the author being asked to kill the
	// mutant, and regenerating it discards the assertion they would add.
	if coverage.IsGeneratedGoSource(src) {
		return nil, nil, nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	reasons, err := annotation.Declarations(path, goComments(fset, file))
	if err != nil {
		return nil, nil, err
	}

	g := &goGen{path: path, src: src, fset: fset, lines: lines, reasons: reasons}
	ast.Inspect(file, g.visit)
	return g.out, g.resolve(), nil
}

// goComments reports the file's comments as the Go parser sees them.
//
// The parser is the authority on what a comment is, so a token inside a block
// comment, after an apostrophe in prose, or inside a string is not one — and a
// line's number comes from the same fileset the mutants are located with, so
// the two cannot drift. Only the line form can carry a declaration, and a
// block comment is left to be recognised and rejected by its text.
func goComments(fset *token.FileSet, file *ast.File) []annotation.Comment {
	var out []annotation.Comment
	for _, group := range file.Comments {
		for _, c := range group.List {
			out = append(out, annotation.Comment{Line: fset.Position(c.Slash).Line, Text: c.Text})
		}
	}
	return out
}

type goGen struct {
	path    string
	src     []byte
	fset    *token.FileSet
	lines   map[int]bool
	reasons map[int]string
	out     []Mutant
	// spans is the line range of each mutant in out, at the same index, so a
	// declaration can be matched against what each one actually replaces.
	spans []lineSpan
}

type lineSpan struct{ first, last int }

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
		g.emitNode(RemoveStatement, node.Pos(), node.Pos(), node.End(), "")
	case *ast.IncDecStmt:
		g.emitNode(RemoveStatement, node.Pos(), node.Pos(), node.End(), "")
	}
	return true
}

// binary emits the operator swaps that apply to one binary expression.
func (g *goGen) binary(node *ast.BinaryExpr) {
	if to, ok := boundary[node.Op]; ok {
		g.emitToken(ConditionalBoundary, node.OpPos, node.Op, to.String())
	}
	if to, ok := negate[node.Op]; ok {
		g.emitToken(NegateConditional, node.OpPos, node.Op, to.String())
	}
	if to, ok := arithmetic[node.Op]; ok {
		g.emitToken(ArithmeticOperator, node.OpPos, node.Op, to.String())
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
				g.emitNode(ReplaceReturn, v.Pos(), v.Pos(), v.End(), "false")
			case "false":
				g.emitNode(ReplaceReturn, v.Pos(), v.Pos(), v.End(), "true")
			}
		case *ast.BasicLit:
			if to, ok := substitute(v); ok {
				g.emitNode(ReplaceReturn, v.Pos(), v.Pos(), v.End(), to)
			}
		}
	}
}

// substitute picks a different value of the same literal kind, so the mutant
// still compiles wherever the original did.
//
// It decides on the literal's *value* and never on its spelling. `0.`, `0e0`
// and `0.0` are one number written three ways, and a rule keyed on the text
// replaces two of them with a third spelling of the same value — a mutant
// nothing can kill, which is then reported as a survivor and fails the gate on
// correct code.
func substitute(lit *ast.BasicLit) (string, bool) {
	switch lit.Kind {
	case token.INT:
		if n, err := strconv.ParseInt(lit.Value, 0, 64); err == nil && n == 0 {
			return "1", true
		}
		return "0", true
	case token.FLOAT:
		if f, err := strconv.ParseFloat(lit.Value, 64); err == nil && f == 0 {
			return "1.0", true
		}
		return "0.0", true
	case token.STRING:
		if s, err := strconv.Unquote(lit.Value); err == nil && s == "" {
			return `"lydite"`, true
		}
		return `""`, true
	}
	return "", false
}

// emitToken records a mutant replacing an operator token. An operator has no
// escapes, so its text measures its source exactly — unlike a literal, whose
// value is not its source. What holds that is the mutant carrying the bytes it
// actually replaced, so a range measured wrongly reports an Original that is
// not the operator.
func (g *goGen) emitToken(op Operator, pos token.Pos, tok token.Token, mutated string) {
	g.add(op, pos, pos, pos+token.Pos(len(tok.String())), mutated)
}

// emitNode records a mutant replacing a node's own source range.
func (g *goGen) emitNode(op Operator, site, from, to token.Pos, mutated string) {
	g.add(op, site, from, to, mutated)
}

// resolve attaches each declaration to the mutants it is about, and reports
// the declarations that turned out to be about nothing.
//
// A declaration covers the mutants whose replaced range contains its line and
// whose range is the shortest of those. Position alone cannot tell what an
// author meant, because one line holds mutants at several scopes: beside
// `println(a < b)` sit two mutants of the comparison and one that deletes the
// whole call. The shortest range is the innermost thing written at that line,
// which is what somebody annotating a line is looking at — so a claim about an
// operator acknowledges the operator, and deleting the call remains a mutant
// they have not answered.
//
// Reaching by containment rather than by a window of lines is what lets a
// declaration inside a multi-line statement work: it sits on no line the
// statement opens or closes on, and it is still written inside it.
//
// The bound the referral bargain rests on is unchanged. Every line of a
// mutant's range is a line the caller asked for, so a declaration inside one is
// on a changed line, which internal/referral sees as added.
func (g *goGen) resolve() []UnmatchedDeclaration {
	var unmatched []UnmatchedDeclaration
	for _, line := range sortedKeys(g.reasons) {
		shortest := -1
		for i, sp := range g.spans {
			if line < sp.first || line > sp.last {
				continue
			}
			if shortest < 0 || g.out[i].Length < shortest {
				shortest = g.out[i].Length
			}
		}
		if shortest < 0 {
			unmatched = append(unmatched, UnmatchedDeclaration{Path: g.path, Line: line, Reason: g.reasons[line]})
			continue
		}
		for i, sp := range g.spans {
			if line >= sp.first && line <= sp.last && g.out[i].Length == shortest {
				g.out[i].Reason = g.reasons[line]
			}
		}
	}
	return unmatched
}

func sortedKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// add records one mutant, after establishing that it edits only requested
// lines and that the range it claims holds what it says it does.
func (g *goGen) add(op Operator, site, from, to token.Pos, mutated string) {
	at, start, end := g.fset.Position(site), g.fset.Position(from), g.fset.Position(to)
	// Every line the splice touches has to be one the caller asked for. A
	// statement can span lines, and gating on its first alone deletes source
	// outside the change.
	for l := start.Line; l <= end.Line; l++ {
		if !g.lines[l] {
			return
		}
	}
	lo, hi := start.Offset, end.Offset
	if lo < 0 || hi > len(g.src) || lo > hi {
		return
	}
	g.out = append(g.out, Mutant{
		Path:     g.path,
		Line:     at.Line,
		Column:   at.Column,
		Operator: op,
		Offset:   lo,
		Length:   hi - lo,
		Original: string(g.src[lo:hi]),
		Mutated:  mutated,
	})
	g.spans = append(g.spans, lineSpan{first: start.Line, last: end.Line})
}
