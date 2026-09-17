// Package flaky is the gate that asks whether a test the change introduces
// agrees with itself.
//
// What it decides first is which tests those are. A test is new when its name
// is declared at HEAD in a scope the merge-base either did not have or had
// without that name — a set difference over names both trees declare, read
// with a parser and never from the diff's added lines. A test moved between
// two files, reindented, or swept along by a formatter is added text that
// declares nothing new, and a gate reading the diff would spend its whole
// budget rerunning tests nobody wrote.
//
// See docs/adr/0039-a-new-test-is-rerun-once-in-its-own-process.md and
// docs/adr/0041-a-new-test-is-rerun-in-rust-and-typescript-too.md.
package flaky

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/treesitter"
)

// ErrNoMergeBase reports that the revision a change is measured against does
// not resolve to a commit, so nothing can be called new.
//
// Named rather than folded into a generic failure, and never an empty result:
// no tests found and no base to compare against are opposite answers, and a
// caller that could not tell them apart would render the gate green over a
// shallow checkout. It is the row's `unmeasured` cause.
var ErrNoMergeBase = errors.New("the merge-base does not resolve to a commit, so no test can be called new")

// maxFileBytes bounds one test file's contribution, matching what
// finding.Source reads. A generated or minified file in a repository lydite
// does not own is not a source of test declarations, and a parse of it is
// memory this gate spends for nothing.
const maxFileBytes = 4 << 20

// Test is one test the change introduces.
//
// Scope is the natural rerun unit of the language it is written in, relative
// to the scan root: the package directory for Go, which is what one `go test`
// process is filtered over, and the component's own directory for Rust and
// TypeScript, which is what one `cargo nextest` or `vitest` run covers. It is
// not the file, since no runner here tells two files in one scope apart and a
// test moved between them is the same test.
//
// Path and Line are where the declaration sits at HEAD, which is what a
// finding anchors to.
type Test struct {
	Scope string
	// Classname is the report's own idea of the suite a test belongs to — a
	// nextest binary, a vitest file — and is empty in a language where a name
	// alone is an identity. It is never derived from the source layout: it is
	// read back out of run 1's report, because computing it would mean
	// reimplementing cargo's and vitest's naming rules against workspace
	// crates, `[[test]] name =` overrides and vitest config nobody here sees.
	Classname string
	Name      string
	Path      string
	Line      int
	// Unreadable says nothing a parser reads can name this test. It is never
	// "new" or "not new" — there is no name to compare against a base tree —
	// so it is unconditionally counted as unmeasurable rather than skipped.
	Unreadable bool
}

// NewTests is every test declared at HEAD that base did not declare, over the
// changed files, sorted by scope and then by name.
//
// dir is the scan root, base a revision already resolved by the caller, lang
// the language whose parser reads the declarations, scope the component's own
// directory relative to the scan root, and changed the paths a change touches
// relative to that root — the whole diff is accepted and filtered here. Only
// the files that can declare a test in lang are parsed: no other file can
// declare a name that was not there before, so the two-tree parse is bounded by
// the change rather than by the repository.
//
// scope is used as given for a language whose rerun unit is the component, and
// ignored for Go, whose unit is the package directory each file sits in.
//
// The HEAD side is read from the working tree, because that is the code the
// rerun will execute and the file a reviewer sees the finding's line in. The
// base side is read out of git, blob by blob, rather than by checking the
// commit out — this needs file contents and nothing else, and a throwaway
// worktree is a checkout of the whole repository to parse a handful of files.
func NewTests(ctx context.Context, dir, base string, lang runner.Lang, scope string, changed []string) ([]Test, error) {
	l, ok := languages[lang]
	if !ok {
		return nil, fmt.Errorf("no parser enumerates the tests of a %s component", lang)
	}
	files := testFiles(l, changed)
	if len(files) == 0 {
		return nil, nil
	}
	if r := executil.RunQuiet(ctx, dir, "git", "rev-parse", "--verify", "--quiet", base+"^{commit}"); !r.Ok() {
		return nil, fmt.Errorf("%w: %q", ErrNoMergeBase, base)
	}
	// Where the scan root sits inside its repository, since a blob is named
	// from the repository root and the caller's paths are relative to the
	// root it scans. A monorepo run as `--dir source` is the shape, and
	// joining the two by hand would read every base file as absent — which
	// reports every test in the change as new.
	prefix, err := gitdiff.Prefix(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("locating the scan root inside the repository: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	// Both sides are gathered per scope before the difference is taken. A name
	// that moved from one changed file to another within one scope is in the
	// scope's base set and its head set, and nets to "not new" only because the
	// union happens first; differencing file by file would report every moved
	// test as new and miss nothing in exchange.
	head := map[string][]Test{}
	was := map[string]map[string]bool{}
	for _, p := range files {
		s := l.scopeOf(p, scope)
		src, err := readWorkTree(root, p)
		if err != nil {
			return nil, err
		}
		if src != nil {
			declared, err := l.read(lang, s, p, src)
			if err != nil {
				return nil, err
			}
			head[s] = append(head[s], declared...)
		}
		src, err = readBlob(ctx, dir, base, path.Join(prefix, p))
		if err != nil {
			return nil, err
		}
		if src == nil {
			// A file the base tree did not have declares nothing there, and
			// every test in it at HEAD is new.
			continue
		}
		declared, err := l.read(lang, s, p, src)
		if err != nil {
			return nil, err
		}
		if was[s] == nil {
			was[s] = map[string]bool{}
		}
		for _, d := range declared {
			was[s][d.Name] = true
		}
	}

	var out []Test
	for s, declared := range head {
		for _, t := range declared {
			// An unreadable declaration is in neither set difference: there is
			// no name to look up in the base tree, so it is carried through as
			// a test the gate will count as unmeasurable rather than as one it
			// decided anything about.
			if t.Unreadable || !was[s][t.Name] {
				out = append(out, t)
			}
		}
	}
	slices.SortFunc(out, func(a, b Test) int {
		return cmp.Or(
			cmp.Compare(a.Scope, b.Scope),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
		)
	})
	return out, nil
}

// language is how one language's test declarations are found and named.
//
// A table rather than a switch, mirroring the language-keyed tables
// internal/treesitter already carries: a language lydite gates and cannot
// enumerate is a missing entry a caller is told about, not a silent empty
// answer.
type language struct {
	// declares reports whether a changed path can declare a test at all. It is
	// what bounds the two-tree parse, and it is narrower than
	// referral.IsTestPath because a file handed to the wrong parser is a parse
	// error reported as a gate that could not run.
	declares func(p string) bool
	// scopeOf is the rerun unit one file's tests belong to, given the
	// component's own directory.
	scopeOf func(p, scope string) string
	// read enumerates the tests one file's bytes declare.
	read func(lang runner.Lang, scope, p string, src []byte) ([]Test, error)
}

var languages = map[runner.Lang]language{
	runner.Go: {
		declares: func(p string) bool { return strings.HasSuffix(path.Base(p), "_test.go") },
		// One `go test` process is filtered over one package, and a package is
		// the directory its files sit in.
		scopeOf: func(p, _ string) string { return path.Dir(p) },
		read: func(_ runner.Lang, scope, p string, src []byte) ([]Test, error) {
			return declaredTests(scope, p, src)
		},
	},
	runner.Rust: {
		// Every changed .rs file and no narrowing by path. Rust's unit tests
		// live inside ordinary source files behind `#[cfg(test)] mod tests`,
		// so a `tests/` convention would miss a test module added to
		// `src/foo.rs` in the same change. A file holding no test code
		// enumerates nothing, which costs one parse.
		declares: func(p string) bool { return isSource(runner.Rust, p) },
		scopeOf:  componentScope,
		read:     declaredByGrammar,
	},
	runner.TypeScript: {
		// The convention every JavaScript runner reads: `.test.`, `.spec.` or
		// a `__tests__/` directory. A source file outside it declares no vitest
		// test, and the extension is asked first so a `README.test.md` never
		// reaches a TypeScript grammar.
		declares: func(p string) bool {
			return isSource(runner.TypeScript, p) && treesitter.TypeScript.TestFile(p)
		},
		scopeOf: componentScope,
		read:    declaredByGrammar,
	},
}

// componentScope is the rerun unit of a language whose runner is invoked once
// per component: every test in it, whatever file it sits in, shares one scope.
func componentScope(_, scope string) string { return scope }

// isSource reports whether p is written in lang, by the extension table
// internal/runner keeps for every language lydite runs.
func isSource(lang runner.Lang, p string) bool {
	l, ok := runner.LangForExt(strings.ToLower(path.Ext(p)))
	return ok && l == lang
}

// declaredByGrammar is the tests one file declares, read off the tree-sitter
// grammars for the languages go/ast does not answer for.
//
// A declaration whose name no parser can state — a `test.each`, a template
// literal, a Rust attribute macro that generates its own names — is carried
// through with Unreadable set rather than dropped. The two answers are
// opposite: a dropped one leaves the row green over a test the gate never
// looked at.
func declaredByGrammar(lang runner.Lang, scope, p string, src []byte) ([]Test, error) {
	declared, err := treesitter.DeclaredTests(lang, p, src)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	out := make([]Test, 0, len(declared))
	for _, d := range declared {
		out = append(out, Test{
			Scope:      scope,
			Name:       d.Name,
			Path:       p,
			Line:       d.Line,
			Unreadable: d.Unreadable,
		})
	}
	return out, nil
}

// testFiles is the subset of changed paths that can declare a test in this
// language, deduplicated and in a stable order.
func testFiles(l language, changed []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range changed {
		if !l.declares(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// readWorkTree reads p under root, answering nil for a file that is not there.
//
// A deleted or renamed-away path is in the diff and not in the tree, and it
// declares nothing at HEAD — which is the whole of what its absence means.
// Reads go through os.Root so a path git reported cannot resolve outside the
// scan root.
func readWorkTree(root *os.Root, p string) ([]byte, error) {
	f, err := root.Open(filepath.FromSlash(p))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("reading %s: larger than %d bytes", p, maxFileBytes)
	}
	return data, nil
}

// readBlob reads repoPath out of rev, answering nil for a path the commit does
// not hold.
//
// Absence and an unreadable blob are asked separately, the way the exemptions
// file is read at the base commit: collapsing them would make a failed read
// indistinguishable from a file the base tree never had, and a file the base
// tree never had makes every test in it new.
func readBlob(ctx context.Context, dir, rev, repoPath string) ([]byte, error) {
	spec := rev + ":" + repoPath
	if r := executil.RunQuiet(ctx, dir, "git", "cat-file", "-e", spec); !r.Ok() {
		return nil, nil
	}
	r := executil.RunQuiet(ctx, dir, "git", "show", spec)
	if !r.Ok() {
		return nil, fmt.Errorf("reading %s: %w: %s", spec, r.Err, strings.TrimSpace(r.Stderr))
	}
	return []byte(r.Output), nil
}

// declaredTests are the top-level test functions src declares, in source
// order.
//
// A file that does not parse is an error rather than a file declaring nothing.
// It cannot compile either, so the suite is failing anyway — but a parse
// failure read as an empty declaration set silently changes the answer: at
// HEAD it hides every new test in the file, and at the base it reports every
// test in the file as new.
func declaredTests(scope, p string, src []byte) ([]Test, error) {
	fset := token.NewFileSet()
	// Comments are not read, so a //go:build line is what it is to go/parser:
	// an ordinary comment. Which files a build tag excludes is answered by the
	// report of what actually ran, never by a parser guessing here.
	file, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	testing := testingName(file)
	if testing == "" {
		return nil, nil
	}
	var out []Test
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !isTestName(fn.Name.Name) || !takesTestingT(fn.Type, testing) {
			continue
		}
		out = append(out, Test{
			Scope: scope,
			Name:  fn.Name.Name,
			Path:  p,
			// fn.Pos() is the `func` keyword, which is the line a reviewer
			// reads the declaration on and the line a finding anchors to.
			Line: fset.Position(fn.Pos()).Line,
		})
	}
	return out, nil
}

// testingName is what the file calls the testing package: its alias where it
// has one, "." for a dot import, and "" for a file that does not import it at
// all and so can declare no test.
func testingName(file *ast.File) string {
	for _, spec := range file.Imports {
		if spec.Path == nil || spec.Path.Value != `"testing"` {
			continue
		}
		if spec.Name != nil {
			if spec.Name.Name == "_" {
				continue
			}
			return spec.Name.Name
		}
		return "testing"
	}
	return ""
}

// isTestName mirrors the rule cmd/go applies: a name prefixed "Test" whose
// next rune is not lower-case, plus "Test" itself.
//
// The same rule and not an approximation of it, because the set this gate
// reruns has to be a subset of the set `go test -run` will actually run. A
// helper named `Testify` matches a looser check and would be filtered for by a
// rerun that then reports "no tests to run".
func isTestName(name string) bool {
	const prefix = "Test"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

// takesTestingT reports whether the signature is a test's: one parameter of
// type *testing.T, no results, no type parameters.
//
// The signature and not the name alone. A helper `func TestHelper(t *testing.T,
// want int)` is not a test, and `FuzzXxx`, `ExampleXxx` and `BenchmarkXxx` are
// outside this slice's definition of new by their names.
func takesTestingT(ft *ast.FuncType, testing string) bool {
	if ft.TypeParams != nil || (ft.Results != nil && len(ft.Results.List) > 0) {
		return false
	}
	if ft.Params == nil || ft.Params.NumFields() != 1 {
		return false
	}
	star, ok := ft.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	if testing == "." {
		id, ok := star.X.(*ast.Ident)
		return ok && id.Name == "T"
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == testing
}
