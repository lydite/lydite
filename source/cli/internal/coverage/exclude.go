package coverage

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"

	"lydite/lydite/internal/annotation"
)

// Excluded is what one Go file declares about a gate: the functions whose
// findings their author says are not evidence, and the declarations that name
// no function at all.
type Excluded struct {
	// Funcs holds each excluded function's declaration and the reason given.
	Funcs map[*ast.FuncDecl]string
	// Unused is the line of every declaration that covers no function.
	//
	// Named rather than dropped, for the reason a mutation declaration
	// covering no mutant is named: its author believes they have answered a
	// finding, and nothing they can see says otherwise. The commonest cause is
	// a declaration written inside a function body rather than above it, where
	// it reads perfectly and does nothing.
	Unused []int
}

// Lines is every line of every excluded function, which is what a measurement
// drops.
//
// The whole declaration and not only its body: the signature line carries the
// first coverage block a Go profile records, so a span that started below it
// would leave that block in the figure the author has just said is not evidence.
func (e Excluded) Lines(fset *token.FileSet) map[int]bool {
	out := map[int]bool{}
	for fn := range e.Funcs {
		for line := fset.Position(fn.Pos()).Line; line <= fset.Position(fn.End()).Line; line++ {
			out[line] = true
		}
	}
	return out
}

// DeclaredExclusions reads one file's declarations for gate.
//
// A declaration covers the function whose **doc comment** holds it, and nothing
// else. The doc comment is where a claim about a function belongs, and it is
// the one comment group a language's own parser already attaches to a
// declaration — so which function a declaration names is Go's answer rather
// than a rule of lydite's that could disagree with the compiler.
//
// It is not proof against every edit, and the limit is worth stating. A
// function written directly beneath an existing declaration, with no blank line
// and no doc comment of its own, takes that declaration: go/parser attaches the
// group to the nearer declaration, so the new function is excluded and the old
// one silently returns to being counted. Nothing refers such a change, because
// the diff adds no line holding the token. What bounds it is that the shape is
// unusual — gofmt-formatted Go separates declarations with a blank line, and a
// new exported function without its own doc comment is itself unusual — and
// that half of the effect is in the safe direction. Closing it properly means
// naming the function in the declaration, which is a grammar change and not
// one this rule can make on its own.
//
// It is here rather than in internal/annotation because that package is a leaf
// that answers what a comment says, not what a language's syntax attaches it
// to — and internal/referral, which decides what merges unread, must keep
// linking neither a parser nor this.
func DeclaredExclusions(fset *token.FileSet, file *ast.File, path string, gate annotation.Gate) (Excluded, error) {
	reasons, err := annotation.Declarations(path, gate, goComments(fset, file))
	if err != nil {
		return Excluded{}, fmt.Errorf("reading %s: %w", path, err)
	}
	out := Excluded{Funcs: map[*ast.FuncDecl]string{}}
	claimed := map[int]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Doc == nil {
			continue
		}
		for _, c := range fn.Doc.List {
			line := fset.Position(c.Pos()).Line
			if reason, ok := reasons[line]; ok {
				out.Funcs[fn] = reason
				claimed[line] = true
			}
		}
	}
	for line := range reasons {
		if !claimed[line] {
			out.Unused = append(out.Unused, line)
		}
	}
	sort.Ints(out.Unused)
	return out, nil
}

// goComments is every comment in a file, as internal/annotation reads them.
//
// Block comments are handed over too and excluded there rather than here: a
// `/*` opens the text, so the token never sits at its start and quoting one in
// documentation silences nothing. Filtering by introducer here would be a
// second place that rule lives.
func goComments(fset *token.FileSet, file *ast.File) []annotation.Comment {
	var out []annotation.Comment
	for _, group := range file.Comments {
		for _, c := range group.List {
			out = append(out, annotation.Comment{Line: fset.Position(c.Pos()).Line, Text: c.Text})
		}
	}
	return out
}

// excludedGoLines is the lines of path that a declaration for gate covers, for
// a caller that has a file on disk rather than an AST in hand.
//
// A file that cannot be read is no exclusion rather than an error: the same
// stance isGeneratedGoFile takes, and for the same reason — the safe direction
// is to keep counting a file lydite cannot classify. A file that will not
// *parse* is treated the same way: it compiled to produce the profile being
// read, so a parse failure here says something is wrong with the tree, and
// failing the aggregate over it would turn a coverage figure into a syntax
// check. What must not be silent is a declaration that is present and
// malformed, which is an error naming the line.
func excludedGoLines(path string, gate annotation.Gate) (map[int]bool, error) {
	src, err := os.ReadFile(path) // #nosec G304 -- the path comes from lydite's own coverage profile, under the scan root
	if err != nil {
		return nil, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, nil
	}
	excluded, err := DeclaredExclusions(fset, file, path, gate)
	if err != nil {
		return nil, err
	}
	return excluded.Lines(fset), nil
}
