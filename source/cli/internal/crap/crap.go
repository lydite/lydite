// Package crap computes the CRAP index for Go: `comp² × (1 − cov)³ + comp` per
// function, where comp is the function's cyclomatic complexity and cov is the
// fraction of its statements a test executed.
//
// It exists because coverage measures execution and nothing else. A test that
// calls a function and asserts nothing scores full marks on every line it
// touches, and a function nobody can follow scores full marks by being called
// once. CRAP is the pair: a simple function is cheap however little of it is
// covered, and a complicated one is expensive until it is tested. The cubed
// term is what makes that a cliff rather than a slope — a complexity-12
// function at 100% is 12, at 50% is 30, and at 0% is 156.
//
// Go alone, and that is a property of the language rather than a stage of the
// work. lydite is Go and walks go/ast in-process, so complexity costs no tool,
// no pin, no install and no staleness risk. Rust and TypeScript have no
// equivalent in hand, and inventing a language-shaped abstraction from one
// implementation would be an abstraction fitted to Go.
//
// Nothing here executes anything or reads a coverage report. The component's
// instrumented run already wrote one and internal/coverage already parsed it
// into per-line hits — including dropping generated files and the blank and
// comment lines a Go profile's block spans sweep up — so this reads that map
// and the source beside it. A second coverage run to answer a second question
// about the same tree is the thing internal/coverage exists to have stopped.
package crap

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lydite/lydite/internal/coverage"
)

// Threshold is the CRAP value above which a function is counted, and it is the
// value Savoia's 2007 definition names. It is deliberately not configurable: a
// knob added before anyone has asked is a knob whose default is the only value
// anyone uses, and the count stored in a baseline is only comparable to one
// taken at the same threshold — so moving it is a change to what is measured,
// which the baseline's own metric-keyed directory is what handles.
const Threshold = 30

// Function is one function's score.
//
// The file and the line identify it, not the name: a bare function name names
// nothing in a multi-package repository, and two packages in one component
// routinely share both a package name and a method name. The path is
// scan-root-relative, so it is the path git names and the path a reader can
// open.
type Function struct {
	// Name is the function as it is written, with a method's receiver:
	// `ParseGoProfile`, `(*Report).Add`.
	Name string
	// File is the file it is declared in, relative to the scan root.
	File string
	// Line is where its declaration starts.
	Line int
	// Complexity is its cyclomatic complexity: one, plus a decision point.
	Complexity int
	// Lines is how many of its statements the report covers, out of how many
	// the report knows about.
	Lines coverage.LineCount
	// Value is the CRAP index.
	Value float64
}

// Report is one component's scores.
type Report struct {
	// Scored is how many functions were scored. A component whose report
	// covers no function at all is unmeasured rather than clean — the two
	// read identically in a count of zero and mean opposite things.
	Scored int
	// Over is every function above Threshold, worst first. The functions
	// rather than their number, because a failing row's job is to name the
	// work: the author clears this gate by testing one of these or by taking
	// it apart.
	Over []Function
	// Worst is the highest value any scored function reached, and is the
	// second of the two scalars a quality ledger records. It is reported and
	// never gated: a change that takes the worst function from 400 to 380 has
	// improved nothing anybody can act on, and one that adds a well-tested
	// complex function raises it without adding a thing to fix.
	Worst float64
}

// Above is how many functions exceed Threshold, and is the scalar the gate
// compares against a baseline.
func (r Report) Above() int { return len(r.Over) }

// Measured reports whether this report describes anything. A component that
// scored no function is unmeasured, never a clean zero.
func (r Report) Measured() bool { return r.Scored > 0 }

// Measure scores every Go function the hits describe.
//
// root is the scan root and hits is what internal/coverage parsed out of the
// component's profile, keyed by scan-root-relative path exactly as git names a
// file. That map is the whole of what bounds this: a file absent from the
// profile is one the component's own tests could never reach, and a generated
// file is already gone from it — the exclusion #16 asks for, taken from the
// one place that implements it rather than written a second time.
//
// A file that cannot be read or parsed is an error naming it, never a file
// quietly skipped. It compiled to produce the profile this is reading, so
// failing to read it now says something is wrong with the tree rather than
// with the code — and a report short one file is a count the gate would
// compare against a baseline taken over all of them.
func Measure(root string, hits coverage.LineHits) (Report, error) {
	files := make([]string, 0, len(hits))
	for file := range hits {
		if strings.HasSuffix(file, ".go") {
			files = append(files, file)
		}
	}
	// Sorted, so a run over one tree produces one report: the worst-first
	// ordering below breaks ties by file and line, and an unsorted walk would
	// otherwise let two runs disagree about which of two equal functions the
	// detail lines name.
	sort.Strings(files)

	var rep Report
	fset := token.NewFileSet()
	for _, file := range files {
		scored, err := scoreFile(fset, root, file, hits[file])
		if err != nil {
			return Report{}, err
		}
		for _, f := range scored {
			rep.Scored++
			rep.Worst = math.Max(rep.Worst, f.Value)
			if f.Value > Threshold {
				rep.Over = append(rep.Over, f)
			}
		}
	}
	sort.Slice(rep.Over, func(i, j int) bool {
		a, b := rep.Over[i], rep.Over[j]
		if a.Value != b.Value {
			return a.Value > b.Value
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return rep, nil
}

// Index is the CRAP formula: `comp² × (1 − cov)³ + comp`.
//
// A fully covered function scores its own complexity, so the threshold is also
// the complexity at which no amount of testing makes a function acceptable.
func Index(complexity int, lines coverage.LineCount) float64 {
	c := float64(complexity)
	uncovered := 1 - lines.Percent()/100
	return c*c*uncovered*uncovered*uncovered + c
}

// span is how much of the report falls inside a function: the lines between
// its first and last, that the report has an entry for at all.
//
// Present-at-all is the denominator rather than every line in the range,
// because a Go profile records blocks and everything between a block's braces
// lands in it — internal/coverage already drops the blank and comment lines
// that sweeps up, and asking the map is what keeps this reading the same
// statements the component's own coverage figure is made of. The two cannot
// disagree about what a line is.
func span(hits map[int]int, start, end int) coverage.LineCount {
	var lines coverage.LineCount
	for line := start; line <= end; line++ {
		count, ok := hits[line]
		if !ok {
			continue
		}
		lines.Total++
		if count > 0 {
			lines.Covered++
		}
	}
	return lines
}

// name is the function as a reader would write it, with a method's receiver so
// two methods of one package are told apart.
func name(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + types(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

// types renders a receiver type, which is a bare identifier or a pointer to
// one. Anything else — a generic receiver's index expression — falls back to
// the underlying name, since the type arguments identify nothing the file and
// line do not.
func types(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + types(t.X)
	case *ast.IndexExpr:
		return types(t.X)
	case *ast.IndexListExpr:
		return types(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return "?"
	}
}

// complexity counts a function's decision points, plus one for the function
// itself: every `if`, `for` and `range`, every non-default `case` and every
// `select` clause that communicates, and every `&&` and `||`.
//
// That is the cyclomatic definition gocyclo and golangci-lint's own cyclop
// already apply to Go, so a number lydite reports and a number a developer
// gets from either agree — which is most of what makes a threshold arguable.
//
// A closure counts towards the function that declares it, because the walk
// descends into it. That is deliberate rather than incidental: the coverage
// half of the score is the function's whole line span, which contains the
// closure's lines, so excluding its branches would score one span's coverage
// against another span's complexity.
func complexity(fn *ast.FuncDecl) int {
	n := 1
	ast.Inspect(fn, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			n++
		case *ast.CaseClause:
			// `default` is not a decision: control reaches it when every
			// other clause has already been decided against.
			if len(x.List) > 0 {
				n++
			}
		case *ast.CommClause:
			if x.Comm != nil {
				n++
			}
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				n++
			}
		}
		return true
	})
	return n
}

// scoreFile scores every function one file declares, above the threshold or
// not. Measure keeps only those over it, because a component's report is one
// row and a list of every function in it is a report nobody reads — but the
// score exists for all of them, since Worst is over every function and not
// over the offenders.
func scoreFile(fset *token.FileSet, root, file string, hits map[int]int) ([]Function, error) {
	path := filepath.Join(root, filepath.FromSlash(file))
	src, err := os.ReadFile(path) // #nosec G304 -- the path comes from lydite's own coverage profile, under the scan root
	if err != nil {
		return nil, fmt.Errorf("reading %s to score its functions: %w", file, err)
	}
	parsed, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing %s to score its functions: %w", file, err)
	}
	var out []Function
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		// A declaration with no body is an assembly or linkname stub. There
		// is nothing to walk and nothing the profile records, so it is not a
		// function that went untested.
		if !ok || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Pos()).Line
		lines := span(hits, start, fset.Position(fn.End()).Line)
		// A function the report knows no line of has no coverage to put in
		// the formula. An empty body is the common one; scoring it as 0%
		// covered would put a complexity-1 function at 2 and count nothing,
		// but a 0/0 that reads as 0% is exactly what
		// coverage.LineCount.Measured exists to keep out of a figure.
		if !lines.Measured() {
			continue
		}
		f := Function{Name: name(fn), File: file, Line: start,
			Complexity: complexity(fn), Lines: lines}
		f.Value = Index(f.Complexity, lines)
		out = append(out, f)
	}
	return out, nil
}
