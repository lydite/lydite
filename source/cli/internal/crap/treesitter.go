package crap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/treesitter"
)

// walked is the extensions scored through a tree-sitter walk, and the language
// whose tables read each one.
//
// `.mts` and `.cts` join `.ts` and `.tsx` because they parse under the same
// TypeScript grammar with nothing JSX-shaped to trip over. `.js`, `.mjs`,
// `.cjs` and `.jsx` are in runner.LangForExt's TypeScript table too, and stay
// out: a JSX file read under the TypeScript tables produces a tree full of
// errors, which this gate reads as a file it could not parse and refuses the
// whole report over. A file in that second group is not silently dropped —
// skipped names it, and Measure carries the list forward rather than letting
// it vanish from a hit map nobody double-checks.
//
// `.py` is the whole of Python's entry: one grammar reads the language, and
// runner.LangForExt claims no second extension for it.
var walked = map[string]bool{".rs": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true, ".py": true}

// tracked reports whether a file the coverage report describes is one this gate
// scores, and which walk scores it.
func tracked(file string) (runner.Lang, bool) {
	ext := strings.ToLower(filepath.Ext(file))
	if !walked[ext] {
		return "", false
	}
	return runner.LangForExt(ext)
}

// skipped reports whether a file belongs to a language this gate otherwise
// scores, but is written in an extension outside walked — the JSX-risking
// TypeScript extensions above, or any later addition to runner.LangForExt's
// tables this gate has not caught up with. Never true for Go, for an extension
// no runner claims at all, or for a language no runner runs (a script), all of which are simply not this gate's
// concern.
func skipped(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	if walked[ext] || ext == ".go" {
		return false
	}
	lang, ok := runner.LangForExt(ext)
	return ok && runner.Runs(lang)
}

// scoreTree scores every function one Rust, TypeScript or Python file declares, the way
// scoreFile scores a Go one: the same span, the same exclusions from both
// gates, and the same formula over a per-language complexity walk.
//
// The functions come from one parse and the declarations from another, rather
// than from a tree threaded between them, because treesitter.DeclaredExclusions
// answers about spans — which are lines, and the same lines whichever parse
// produced them.
func scoreTree(lang runner.Lang, root, file string, hits map[int]int) (fileScore, error) {
	var out fileScore
	g, ok := treesitter.GrammarFor(lang, file)
	if !ok {
		return out, treesitter.ErrNoGrammar{Lang: lang}
	}
	// Test code is scored by nothing, so it is not read for declarations
	// either: a declaration inside it names a function this gate would never
	// have scored, and reporting it unused would be advice to move an
	// annotation that is already where it belongs.
	if g.TestFile(file) {
		return out, nil
	}
	path := filepath.Join(root, filepath.FromSlash(file))
	src, err := os.ReadFile(path) // #nosec G304 -- the path comes from lydite's own coverage report, under the scan root
	if err != nil {
		return out, fmt.Errorf("reading %s to score its functions: %w", file, err)
	}
	funcs, language, err := treesitter.ScoredFunctions(lang, file, src)
	if err != nil {
		return out, fmt.Errorf("parsing %s to score its functions: %w", file, err)
	}
	// Both gates' declarations, and the union of them, for the reason
	// scoreFile gives: a coverage declaration has already taken its function's
	// lines out of the hit map, so read here it is excluded and counted rather
	// than dropping out uncounted. Only the CRAP declarations are held to
	// covering a function, because only they are this gate's to diagnose.
	declared, err := treesitter.DeclaredExclusions(lang, file, src, annotation.CRAP)
	if err != nil {
		return out, err
	}
	uncovered, err := treesitter.DeclaredExclusions(lang, file, src, annotation.Coverage)
	if err != nil {
		return out, err
	}
	for _, line := range declared.Unused {
		out.unused = append(out.unused, fmt.Sprintf("%s:%d", file, line))
	}
	for _, fn := range funcs {
		_, byScore := declared.Funcs[fn.Span]
		_, byCoverage := uncovered.Funcs[fn.Span]
		if byScore || byCoverage {
			out.excluded++
			continue
		}
		lines := scoredLines(hits, fn)
		// A function the report knows no line of has no coverage to put in the
		// formula, the same as in Go: a 0/0 read as 0% is what
		// coverage.LineCount.Measured exists to keep out of a figure.
		if !lines.Measured() {
			continue
		}
		f := Function{Name: fn.Name, File: file, Line: fn.Span.First,
			Complexity: complexityOf(g, fn, language), Lines: lines}
		f.Value = Index(f.Complexity, lines)
		out.scored = append(out.scored, f)
	}
	return out, nil
}

// scoredLines is how much of the report falls inside a function, less every
// line belonging to a unit nested in it.
//
// A nested `fn` or a nested `function` declaration is scored in its own right,
// so its lines are evidence about it — and no line is evidence about two
// functions at once. A closure or a callback arrow is not a unit and never
// appears in Nested, so its lines stay where its branches are counted.
func scoredLines(hits map[int]int, f treesitter.Func) coverage.LineCount {
	lines := span(hits, f.Span.First, f.Span.Last)
	for _, nested := range f.Nested {
		inner := span(hits, nested.Span.First, nested.Span.Last)
		lines.Total -= inner.Total
		lines.Covered -= inner.Covered
	}
	return lines
}

// complexityOf is the walk the grammar's own counting rules are written for.
//
// Exhaustive, and loud where it is not. A grammar reaching a fallback branch
// would be counted by another language's rules — no error, no parse failure,
// a plausible number under a heading naming the wrong language — which is the
// one failure in this package nothing downstream can detect. The grammar comes
// from treesitter.GrammarFor over a language this gate's own walked table
// admits, so an unhandled one is a lydite bug and not an input.
func complexityOf(g treesitter.Grammar, f treesitter.Func, language *gotreesitter.Language) int {
	switch g {
	case treesitter.Rust:
		return complexityRust(f, language)
	case treesitter.TypeScript, treesitter.TSX:
		// TSX is the TypeScript grammar plus JSX, and JSX adds no decision
		// point of its own: the tables are named separately here because
		// GrammarFor hands this function the raw grammar, not the tables it
		// shares.
		return complexityTypeScript(f, language)
	case treesitter.Python:
		return complexityPython(f, language)
	default:
		panic(fmt.Sprintf("crap: no complexity counting rule for grammar %d", g))
	}
}

// complexityRust counts a Rust function's decision points, plus one for the
// function itself: every `if`, `while`, `for` and `loop`, every `match` arm
// that is not the literal `_` wildcard, every `&&` and `||`, and every `?`.
//
// `if let` and `while let` need no rule of their own — the grammar puts the
// pattern-matching and the boolean form in one node with a different condition
// child — and neither does an `else if` chain, which is an `if_expression`
// inside the outer's `else` branch that the walk reaches like any other.
//
// `loop` counts although it branches nowhere by itself, because Go's own
// complexity() counts a bare `for {}`: the number's job is to agree with what a
// developer's own tooling reports, not to be theoretically pure. `?` counts
// because it is the `if err != nil { return err }` a Go author writes out, and
// eliding the syntax should not erase the branch.
func complexityRust(f treesitter.Func, language *gotreesitter.Language) int {
	return walkComplexity(f, language, func(n *gotreesitter.Node) int {
		switch n.Type(language) {
		case "if_expression", "while_expression", "for_expression", "loop_expression", "try_expression":
			return 1
		case "match_arm":
			// Only the wildcard token itself is the arm control reaches once
			// every other has been decided against, which is what Go's
			// complexity() says about `default`. A catch-all binding —
			// `other => ...` — is an arm like any other, since telling it from
			// one that matches something is exhaustiveness analysis lydite
			// does not do.
			if wildcard(n, language) {
				return 0
			}
			return 1
		case "binary_expression":
			return logical(n, language, "&&", "||")
		}
		return 0
	})
}

// wildcard reports whether a `match` arm's pattern is the literal `_`, and
// nothing else: a guarded `_ if n > 0` is a decision and carries a child the
// bare wildcard does not.
func wildcard(arm *gotreesitter.Node, language *gotreesitter.Language) bool {
	pattern := arm.ChildByFieldName("pattern", language)
	return pattern != nil && pattern.ChildCount() == 1 && pattern.Child(0).Type(language) == "_"
}

// complexityTypeScript counts a TypeScript or TSX function's decision points,
// plus one for the function itself: every node ESLint's own `complexity` rule
// increments for in its default mode.
//
// ESLint rather than a narrower rule shaped like Go's, for the reason Go's own
// walk follows gocyclo: the number lydite reports and the number a developer
// gets from the tool already in their editor should agree. Optional chaining
// and a default parameter are in that set and stay in it — both desugar to a
// real branch, `a?.b` to `a == null ? undefined : a.b` and `x = 1` to
// `x === undefined ? 1 : x`.
func complexityTypeScript(f treesitter.Func, language *gotreesitter.Language) int {
	return walkComplexity(f, language, func(n *gotreesitter.Node) int {
		switch n.Type(language) {
		case "if_statement", "while_statement", "do_statement", "for_statement", "for_in_statement",
			"catch_clause", "ternary_expression":
			// `for_in_statement` is `for-of` too: one node in this grammar,
			// told apart by the keyword between its pattern and its subject.
			return 1
		case "switch_case":
			// `switch_default` is its own node type, so `default` is excluded
			// by not being named here rather than by a test on this one.
			return 1
		case "optional_chain":
			// `a?.b`. The call form `f?.()` writes its `?.` straight into the
			// call rather than into this node, and is counted there.
			return 1
		case "call_expression":
			return optionalCall(n, language)
		case "assignment_pattern", "object_assignment_pattern":
			// A default written into a destructuring pattern: `[a = 1]`,
			// `{ count = 0 }`.
			return 1
		case "required_parameter", "optional_parameter":
			if n.ChildByFieldName("value", language) != nil {
				return 1
			}
		case "binary_expression":
			return logical(n, language, "&&", "||", "??")
		case "augmented_assignment_expression":
			return logical(n, language, "&&=", "||=", "??=")
		}
		return 0
	})
}

// complexityPython counts a Python function's decision points, plus one for
// the function itself: every node radon's own cyclomatic metric increments
// for, in its default mode.
//
// radon rather than mccabe, which flake8 carries, for the reason Go's walk
// follows gocyclo and TypeScript's follows ESLint — the number lydite reports
// and the number a developer's own tooling reports should agree — and because
// mccabe counts no `and`, no `or`, no ternary, no comprehension and no `match`
// at all, so agreeing with it would make Python the one language here whose
// short-circuiting operators are free.
//
// An `else` is counted on a loop and on a `try` and not on an `if`. `for ...
// else` runs its else exactly when the loop finished without `break` and `try
// ... else` exactly when nothing was raised, which are decisions; an `if`'s
// else is the unconditional remainder, the same thing Go's own complexity()
// says about a `default` clause.
//
// `assert` counts. It is `if not x: raise AssertionError` with the branch
// spelled shorter, the same ground ADR 0036 counts Rust's `?` on. radon counts
// it too, and offers `--no-assert` to stop — the default is the number a
// developer sees.
//
// `with` counts nothing: it is a call and a cleanup, and no control flow
// chooses between two paths there.
func complexityPython(f treesitter.Func, language *gotreesitter.Language) int {
	return walkComplexity(f, language, func(n *gotreesitter.Node) int {
		switch n.Type(language) {
		case "if_statement", "elif_clause", "for_statement", "while_statement",
			"except_clause", "conditional_expression", "assert_statement":
			// `async for` is `for_statement` and `async def` is
			// `function_definition`: the grammar writes `async` as an extra
			// token rather than as a node type of its own.
			return 1
		case "else_clause":
			return loopOrTryElse(n, language)
		case "for_in_clause", "if_clause":
			// A comprehension's own loop and its filter: `[x for x in xs if
			// x]` iterates and decides, and writing it on one line does not
			// remove either. `if_clause` is also how a `match` case writes its
			// guard, which the case itself already counts.
			return comprehensionClause(n, language)
		case "case_clause":
			if pythonWildcard(n, language) {
				return 0
			}
			return 1
		case "boolean_operator":
			return logical(n, language, "and", "or")
		}
		return 0
	})
}

// pythonComprehensions are the node types whose clauses are a loop over a
// sequence rather than a pattern-matching guard.
var pythonComprehensions = map[string]bool{
	"list_comprehension":       true,
	"set_comprehension":        true,
	"dictionary_comprehension": true,
	"generator_expression":     true,
}

// comprehensionClause counts a `for` or `if` clause written inside a
// comprehension, and nothing written inside a `match` case, whose guard shares
// the if_clause node type and is already counted by the case it guards.
func comprehensionClause(n *gotreesitter.Node, language *gotreesitter.Language) int {
	parent := n.Parent()
	if parent == nil || !pythonComprehensions[parent.Type(language)] {
		return 0
	}
	return 1
}

// loopOrTryElse counts the `else` a loop or a `try` carries and not the one an
// `if` does, which is the only thing telling them apart: the grammar gives all
// three the same node type and says which is which by what holds it.
func loopOrTryElse(n *gotreesitter.Node, language *gotreesitter.Language) int {
	parent := n.Parent()
	if parent == nil {
		return 0
	}
	switch parent.Type(language) {
	case "for_statement", "while_statement", "try_statement":
		return 1
	}
	return 0
}

// pythonWildcard reports whether a `match` case is the bare, unguarded `case
// _:` — the one control reaches once every other case has been decided
// against, and so no decision of its own.
//
// A guard makes it one again: `case _ if ready:` can be reached and declined.
// The guard is a sibling of the pattern in this grammar rather than part of
// it, which is why it is looked for on the clause and not inside the pattern
// the way Rust's arm carries its own.
func pythonWildcard(clause *gotreesitter.Node, language *gotreesitter.Language) bool {
	bare := false
	for i := range clause.ChildCount() {
		switch child := clause.Child(i); child.Type(language) {
		case "case_pattern":
			bare = child.ChildCount() == 1 && child.Child(0).Type(language) == "_"
		case "if_clause":
			return false
		}
	}
	return bare
}

// optionalCall counts the `?.` an optionally-chained call writes as its own
// child: `f?.()` is a call whose `?.` belongs to the call, where `a?.b` wraps
// the same token in an `optional_chain` node. Counting both node types
// unconditionally would score `a?.b` twice.
func optionalCall(call *gotreesitter.Node, language *gotreesitter.Language) int {
	for i := range call.ChildCount() {
		if call.Child(i).Type(language) == "?." {
			return 1
		}
	}
	return 0
}

// logical reports whether an operator node carries one of the short-circuiting
// operators, each of which is a branch the expression may not evaluate.
func logical(n *gotreesitter.Node, language *gotreesitter.Language, ops ...string) int {
	operator := n.ChildByFieldName("operator", language)
	if operator == nil {
		return 0
	}
	for _, op := range ops {
		if operator.Type(language) == op {
			return 1
		}
	}
	return 0
}

// walkComplexity is one plus what count says about every node in the function's
// subtree, plus a flat one per unit nested in it.
//
// The walk stops at each nested unit and descends into everything else. A
// nested named function is scored in its own right, so counting its branches
// here too would score them twice; the flat one left behind says the outer
// function contains it, where inheriting the nested function's own value would
// compound outward through arbitrary depth for no reason connected to what the
// outer function does. A closure, a callback arrow and an anonymous
// `function (x) {...}` are not units, so the walk goes straight through them
// and their branches count here — which is what keeps the coverage this
// complexity is scored against the span those branches are in.
func walkComplexity(f treesitter.Func, language *gotreesitter.Language, count func(*gotreesitter.Node) int) int {
	n := 1 + len(f.Nested)
	nested := make(map[*gotreesitter.Node]bool, len(f.Nested))
	for _, unit := range f.Nested {
		nested[unit.Node] = true
	}
	var visit func(node *gotreesitter.Node)
	visit = func(node *gotreesitter.Node) {
		if node == nil || nested[node] {
			return
		}
		n += count(node)
		for i := range node.ChildCount() {
			visit(node.Child(i))
		}
	}
	visit(f.Node)
	return n
}
