// Package flaky is the gate that asks whether a test the change introduces
// agrees with itself.
//
// What it decides first is which tests those are. A test is new when its name
// is declared at HEAD in a package the merge-base either did not have or had
// without that name — a set difference over names both trees declare, read
// with go/ast and never from the diff's added lines. A test moved between two
// files, reindented, or swept along by a formatter is added text that declares
// nothing new, and a gate reading the diff would spend its whole budget
// rerunning tests nobody wrote.
//
// See docs/adr/0039-a-new-test-is-rerun-once-in-its-own-process.md.
package flaky

import (
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
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitdiff"
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

// Test is one top-level test function the change introduces.
//
// Package is the directory holding it, relative to the scan root, and together
// with Name it is the test's identity — not the file, since `go test -run`
// cannot tell two files in one package apart and a test moved from a_test.go
// to b_test.go is the same test. Path and Line are where the declaration sits
// at HEAD, which is what a finding anchors to.
type Test struct {
	Package string
	Name    string
	Path    string
	Line    int
}

// NewTests is every test declared at HEAD that base did not declare, over the
// changed files, sorted by package and then by name.
//
// dir is the scan root, base a revision already resolved by the caller, and
// changed the paths a change touches relative to that root — the whole diff is
// accepted and filtered here. Only the test files the diff touched are parsed:
// no other file can declare a name that was not there before, so the two-tree
// parse is bounded by the change rather than by the repository.
//
// The HEAD side is read from the working tree, because that is the code the
// rerun will execute and the file a reviewer sees the finding's line in. The
// base side is read out of git, blob by blob, rather than by checking the
// commit out — this needs file contents and nothing else, and a throwaway
// worktree is a checkout of the whole repository to parse a handful of files.
func NewTests(ctx context.Context, dir, base string, changed []string) ([]Test, error) {
	files := goTestFiles(changed)
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

	// Both sides are gathered per package before the difference is taken. A
	// name that moved from one changed file to another within one package is
	// in the package's base set and its head set, and nets to "not new" only
	// because the union happens first; differencing file by file would report
	// every moved test as new and miss nothing in exchange.
	head := map[string][]Test{}
	was := map[string]map[string]bool{}
	for _, p := range files {
		pkg := path.Dir(p)
		src, err := readWorkTree(root, p)
		if err != nil {
			return nil, err
		}
		if src != nil {
			declared, err := declaredTests(pkg, p, src)
			if err != nil {
				return nil, err
			}
			head[pkg] = append(head[pkg], declared...)
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
		declared, err := declaredTests(pkg, p, src)
		if err != nil {
			return nil, err
		}
		if was[pkg] == nil {
			was[pkg] = map[string]bool{}
		}
		for _, d := range declared {
			was[pkg][d.Name] = true
		}
	}

	var out []Test
	for pkg, declared := range head {
		for _, t := range declared {
			if !was[pkg][t.Name] {
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package // [lydite:exclude_from_mutation][reached only when the guard above already found the two packages unequal, so <= behaves exactly like < here]
		}
		return out[i].Name < out[j].Name // [lydite:exclude_from_mutation][reached only when out[i].Package == out[j].Package, and the compiler already forbids two tests sharing a name within one package — the two names being compared are always distinct, so <= behaves exactly like < here]
	})
	return out, nil
}

// goTestFiles is the subset of changed paths that can declare a Go test,
// deduplicated and in a stable order.
//
// Narrower than referral.IsTestPath on purpose: that one answers "is this a
// test file" for all three ecosystems lydite gates, and this slice is Go only.
// A `tests/helper.rs` reaching go/parser is a parse error reported as a gate
// that could not run.
func goTestFiles(changed []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range changed {
		if !strings.HasSuffix(path.Base(p), "_test.go") || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
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
func declaredTests(pkg, p string, src []byte) ([]Test, error) {
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
			Package: pkg,
			Name:    fn.Name.Name,
			Path:    p,
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
