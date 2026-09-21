package coverage

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/treesitter"
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
			if decl, ok := reasons[line]; ok {
				out.Funcs[fn] = decl.Reason
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

// exclusions is what one source file declares about a gate, in the form a
// measurement reads it: the lines a declaration covers, and the declarations
// that cover no function at all.
//
// Both halves travel together because both come out of one parse, and because
// a caller that took only the lines would drop every unmatched declaration in
// silence — which is the state its author cannot see and most needs told.
type exclusions struct {
	// Lines is every line of every excluded function, which is what a
	// measurement drops from both sides of its figure.
	Lines map[int]bool
	// Unused names each declaration that covers no function, as "file:line"
	// against the path the report keys that file by, so a reader can open it.
	Unused []string
}

// excludedGoLines is the lines of path that a declaration for gate covers, for
// a caller that has a file on disk rather than an AST in hand. name is what the
// report keys this file by, which is what an unused declaration is named
// against — path is where it sits on this machine, and no reader is looking
// there.
//
// A file that cannot be read is no exclusion rather than an error: the same
// stance isGeneratedGoFile takes, and for the same reason — the safe direction
// is to keep counting a file lydite cannot classify. A file that will not
// *parse* is treated the same way: it compiled to produce the profile being
// read, so a parse failure here says something is wrong with the tree, and
// failing the aggregate over it would turn a coverage figure into a syntax
// check. What must not be silent is a declaration that is present and
// malformed, which is an error naming the line.
func excludedGoLines(path, name string, gate annotation.Gate) (exclusions, error) {
	src, err := os.ReadFile(path) // #nosec G304 -- the path comes from lydite's own coverage profile, under the scan root
	if err != nil {
		return exclusions{}, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return exclusions{}, nil
	}
	excluded, err := DeclaredExclusions(fset, file, path, gate)
	if err != nil {
		return exclusions{}, err
	}
	return exclusions{Lines: excluded.Lines(fset), Unused: named(name, excluded.Unused)}, nil
}

// lcovExclusions is what one source file behind an lcov trace declares about
// gate: the parser's answer for a language lydite holds tree-sitter tables for,
// and nothing at all for a language it does not.
//
// Python is the second kind, and the consequence is that
// `[lydite:exclude_from_coverage]` is not available in a Python component.
// A declaration's reach is the span of the function it sits above, only a parser
// can say where that span ends, and internal/treesitter has no Python tables —
// so asking for its exclusions answers ErrNoGrammar for every file the report
// names, which would fail the measurement of a component over a feature the
// language does not have. Skipped here rather than absorbed in
// excludedLCOVLines, whose contract stays what it reads for the two languages
// that do have tables: a file those tables cannot parse is no exclusion, and
// every other answer from the parser — a declaration with no reason, above all —
// is still an error that fails the measurement.
//
// GrammarFor is asked per file, not per language, because it is the one place
// that says which tables read a path — a .tsx file and a .ts file are one
// language and two grammars.
func lcovExclusions(path, name string, lang runner.Lang, gate annotation.Gate) (exclusions, error) {
	if _, ok := treesitter.GrammarFor(lang, path); !ok {
		return exclusions{}, nil
	}
	return excludedLCOVLines(path, name, lang, gate)
}

// excludedLCOVLines is the lines of one Rust or TypeScript source file that a
// declaration for gate covers. name is what the report keys the file by, as it
// is for Go.
//
// The same question excludedGoLines answers, asked of a language whose report
// is lcov. It is a parser that answers it in both, because the alternative is a
// declaration meaning one thing in Go and another here — see ADR 0034. lcov
// carries a start line per function and no end, so the report itself cannot say
// how far a declaration reaches.
//
// A file that cannot be read or will not parse is no exclusion rather than an
// error, the stance excludedGoLines takes and for the same reason: the tree
// compiled to produce the report being read, so failing a coverage figure over
// a parse would turn it into a syntax check. A declaration that is present and
// malformed is still an error naming the line.
func excludedLCOVLines(path, name string, lang runner.Lang, gate annotation.Gate) (exclusions, error) {
	src, err := os.ReadFile(path) // #nosec G304,G703 -- measureLCOV, the one route here, has already checked that this path resolves inside the component's own directory
	if err != nil {
		return exclusions{}, nil
	}
	declared, err := treesitter.DeclaredExclusions(lang, path, src, gate)
	if err != nil {
		var unparsed treesitter.ErrUnparsed
		if errors.As(err, &unparsed) {
			return exclusions{}, nil
		}
		return exclusions{}, err
	}
	return exclusions{Lines: declared.Lines(), Unused: named(name, declared.Unused)}, nil
}

// named renders a file's unused declaration lines the way every gate names a
// site, so a coverage warning and a CRAP one read alike.
func named(file string, lines []int) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, fmt.Sprintf("%s:%d", file, line))
	}
	return out
}
