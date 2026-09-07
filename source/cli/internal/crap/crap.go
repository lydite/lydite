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
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"lydite/lydite/internal/annotation"
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
	// Excluded is how many functions their author declared this score is not
	// evidence about, and it is reported beside the count for that reason: a
	// repository can annotate its way to nothing above the threshold, and this
	// is the number that makes it visible when one does.
	Excluded int
	// Unused names each declaration that covers no function, so an author who
	// believes they have answered a score is told when nothing they wrote did.
	Unused []string
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
	// Sorted, so a run over one tree produces one report. The ordering below
	// is a total order and does not need it; what does is the *first* failure,
	// since a tree with two unreadable files would otherwise name a different
	// one on each run and a reader would be chasing a moving target.
	slices.Sort(files)

	var rep Report
	fset := token.NewFileSet()
	for _, file := range files {
		one, err := scoreFile(fset, root, file, hits[file])
		if err != nil {
			return Report{}, err
		}
		rep.Excluded += one.excluded
		rep.Unused = append(rep.Unused, one.unused...)
		for _, f := range one.scored {
			rep.Scored++
			rep.Worst = math.Max(rep.Worst, f.Value)
			if f.Value > Threshold {
				rep.Over = append(rep.Over, f)
			}
		}
	}
	// Worst first, then by where it is. The tie-break is what makes the order
	// a total one, so two functions scoring the same are not reported in
	// whichever order the walk happened to reach them — and it is written as a
	// chain of comparisons rather than as a cascade of `!=` guards, because a
	// guard that has already established inequality leaves the comparison
	// under it unable to be wrong.
	slices.SortFunc(rep.Over, func(a, b Function) int {
		return cmp.Or(
			cmp.Compare(b.Value, a.Value),
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
		)
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

// fileScore is what one file contributed: the functions it scored, how many it
// did not because their author declared them, and any declaration that
// documented no function.
//
// One value rather than three results beside an error, so a failure returns the
// zero of it. Three zero literals on an error path are three values no caller
// reads — every one of them free to be anything at all, and nothing able to
// notice.
type fileScore struct {
	scored   []Function
	excluded int
	unused   []string
}

// scoreFile scores every function one file declares, above the threshold or
// not. Measure keeps only those over it, because a component's report is one
// row and a list of every function in it is a report nobody reads — but the
// score exists for all of them, since Worst is over every function and not
// over the offenders.
func scoreFile(fset *token.FileSet, root, file string, hits map[int]int) (fileScore, error) {
	var out fileScore
	path := filepath.Join(root, filepath.FromSlash(file))
	src, err := os.ReadFile(path) // #nosec G304 -- the path comes from lydite's own coverage profile, under the scan root
	if err != nil {
		return out, fmt.Errorf("reading %s to score its functions: %w", file, err)
	}
	// With comments, because a function's doc comment is where its author says
	// this score is not evidence about it.
	parsed, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return out, fmt.Errorf("parsing %s to score its functions: %w", file, err)
	}
	// Both gates' declarations, and the union of them.
	//
	// A CRAP declaration is the direct statement: this score is not evidence.
	// A coverage one reaches here because the lines it excludes are already
	// gone from the hit map, so the function scores nothing measurable and
	// would drop out silently — uncounted, which is the one thing the excluded
	// count exists to prevent. Read here it is excluded *and* counted, so
	// neither token takes a function out of the figure without saying so.
	//
	// Only the CRAP declarations are held to covering a function, because only
	// they are this gate's to diagnose: a coverage declaration that documents
	// nothing is the coverage gate's warning to give, and giving it twice
	// would have one typo reported by two gates.
	declared, err := coverage.DeclaredExclusions(fset, parsed, file, annotation.CRAP)
	if err != nil {
		return out, err
	}
	uncovered, err := coverage.DeclaredExclusions(fset, parsed, file, annotation.Coverage)
	if err != nil {
		return out, err
	}
	for _, line := range declared.Unused {
		out.unused = append(out.unused, fmt.Sprintf("%s:%d", file, line))
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		// A declaration with no body is an assembly or linkname stub. There
		// is nothing to walk and nothing the profile records, so it is not a
		// function that went untested.
		if !ok || fn.Body == nil {
			continue
		}
		_, byScore := declared.Funcs[fn]
		_, byCoverage := uncovered.Funcs[fn]
		if byScore || byCoverage {
			out.excluded++
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
		out.scored = append(out.scored, f)
	}
	return out, nil
}
