package flaky

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/runner"
)

// TestTheProbeAtTwoRevisions reads the set difference off the fixture ADR 0039
// decides on: base declares TestPreExistingStable alone, head adds three.
func TestTheProbeAtTwoRevisions(t *testing.T) {
	r := newRepo(t)
	r.copy(fixture.Tree(t, filepath.Join("testdata", "flakyprobe", "base")))
	base := r.commit("base")
	r.copy(fixture.Tree(t, filepath.Join("testdata", "flakyprobe", "head")))
	r.commit("head")

	// The paths a caller hands NewTests are the ones git reports, so this one
	// takes them from git rather than restating them.
	changed, err := gitdiff.Changed(t.Context(), r.dir, base)
	if err != nil {
		t.Fatalf("gitdiff.Changed: %v", err)
	}
	got, err := NewTests(t.Context(), r.dir, base, runner.Go, ".", changed.All)
	if err != nil {
		t.Fatalf("NewTests: %v", err)
	}
	want := []Test{
		{Scope: ".", Name: "TestDeterministicNew", Path: "probe_test.go", Line: 23},
		{Scope: ".", Name: "TestFlakyNew", Path: "probe_test.go", Line: 32},
		{Scope: ".", Name: "TestNewSubtests", Path: "probe_test.go", Line: 44},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestMovedWithinAPackageIsNotNew is the case the package-plus-name identity
// exists for: a test whose file changed and whose name did not.
func TestMovedWithinAPackageIsNotNew(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"pkg/a_test.go": source("TestMoved")})
	base := r.commit("base")
	r.remove("pkg/a_test.go")
	r.write(map[string]string{"pkg/b_test.go": source("TestMoved")})
	r.commit("head")

	got := mustNewTests(t, r.dir, base, "pkg/a_test.go", "pkg/b_test.go")
	if len(got) != 0 {
		t.Errorf("NewTests = %+v, want none: the name is declared at both revisions in one package", got)
	}
}

// TestRenamedTestIsNew: the old name leaves the head set and the new one was
// never in the base set, which is what a set difference says about a rename.
func TestRenamedTestIsNew(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"pkg/a_test.go": source("TestOldName")})
	base := r.commit("base")
	r.write(map[string]string{"pkg/a_test.go": source("TestNewName")})
	r.commit("head")

	got := mustNewTests(t, r.dir, base, "pkg/a_test.go")
	want := []Test{{Scope: "pkg", Name: "TestNewName", Path: "pkg/a_test.go", Line: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestAMovedPackageReportsEveryTestInItNew records the over-reporting ADR 0039
// accepts: the identity carries the directory, so a package that moves has no
// base set to difference against.
func TestAMovedPackageReportsEveryTestInItNew(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"old/a_test.go": source("TestStable")})
	base := r.commit("base")
	r.remove("old/a_test.go")
	r.write(map[string]string{"new/a_test.go": source("TestStable")})
	r.commit("head")

	got := mustNewTests(t, r.dir, base, "old/a_test.go", "new/a_test.go")
	want := []Test{{Scope: "new", Name: "TestStable", Path: "new/a_test.go", Line: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestAnAddedFileContributesEveryTestInIt, and a deleted one contributes
// nothing: a file the head tree does not hold declares nothing at head.
func TestAnAddedFileContributesEveryTestInIt(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"pkg/gone_test.go": source("TestDeleted")})
	base := r.commit("base")
	r.remove("pkg/gone_test.go")
	r.write(map[string]string{"pkg/added_test.go": source("TestOne", "TestTwo")})
	r.commit("head")

	got := mustNewTests(t, r.dir, base, "pkg/added_test.go", "pkg/gone_test.go")
	want := []Test{
		{Scope: "pkg", Name: "TestOne", Path: "pkg/added_test.go", Line: 5},
		{Scope: "pkg", Name: "TestTwo", Path: "pkg/added_test.go", Line: 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestOnlyATestSignatureCounts: the unit is the top-level func TestXxx(t
// *testing.T), so a helper, a fuzz target, an example, a benchmark and a
// method carrying the name are all outside it.
func TestOnlyATestSignatureCounts(t *testing.T) {
	const head = `package pkg

import "testing"

type suite struct{}

func TestReal(t *testing.T) {}

func TestHelper(t *testing.T, want int) {}

func TestResult(t *testing.T) error { return nil }

func Testify(t *testing.T) {}

func TestGeneric[T any](t *testing.T) {}

func (s suite) TestMethod(t *testing.T) {}

func FuzzTarget(f *testing.F) {}

func ExampleThing() {}

func BenchmarkThing(b *testing.B) {}
`
	r := newRepo(t)
	r.write(map[string]string{"pkg/a_test.go": "package pkg\n"})
	base := r.commit("base")
	r.write(map[string]string{"pkg/a_test.go": head})
	r.commit("head")

	got := mustNewTests(t, r.dir, base, "pkg/a_test.go")
	want := []Test{{Scope: "pkg", Name: "TestReal", Path: "pkg/a_test.go", Line: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestTheTestingImportIsReadFromTheFile, so an aliased or dotted import still
// names a test and a file importing nothing named testing declares none.
func TestTheTestingImportIsReadFromTheFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "aliased",
			src:  "package pkg\n\nimport tst \"testing\"\n\nfunc TestAliased(t *tst.T) {}\n",
			want: []string{"TestAliased"},
		},
		{
			name: "dot import",
			src:  "package pkg\n\nimport . \"testing\"\n\nfunc TestDotted(t *T) {}\n",
			want: []string{"TestDotted"},
		},
		{
			name: "testing not imported",
			src:  "package pkg\n\ntype T struct{}\n\nfunc TestLookalike(t *T) {}\n",
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t)
			r.write(map[string]string{"pkg/a_test.go": "package pkg\n"})
			base := r.commit("base")
			r.write(map[string]string{"pkg/a_test.go": tc.src})
			r.commit("head")

			var got []string
			for _, test := range mustNewTests(t, r.dir, base, "pkg/a_test.go") {
				got = append(got, test.Name)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NewTests named %v, want %v", got, tc.want)
			}
		})
	}
}

// TestABuildTaggedFileIsParsed: which files a tag excludes is answered by the
// report of what ran, so the parse here must not choke on the constraint.
func TestABuildTaggedFileIsParsed(t *testing.T) {
	head := "//go:build integration\n\npackage pkg\n\nimport \"testing\"\n\nfunc TestTagged(t *testing.T) {}\n"
	r := newRepo(t)
	r.write(map[string]string{"pkg/a_test.go": "package pkg\n"})
	base := r.commit("base")
	r.write(map[string]string{"pkg/a_test.go": head})
	r.commit("head")

	got := mustNewTests(t, r.dir, base, "pkg/a_test.go")
	want := []Test{{Scope: "pkg", Name: "TestTagged", Path: "pkg/a_test.go", Line: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestOnlyGoTestFilesAreParsed: the slice is Go only, and a path from another
// ecosystem reaching go/parser would fail the gate over a file that cannot
// declare a Go test.
func TestOnlyGoTestFilesAreParsed(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"pkg/keep.txt": "x\n"})
	base := r.commit("base")
	r.write(map[string]string{
		"pkg/lib.go":       "package pkg\n\nfunc Sum(a, b int) int { return a + b }\n",
		"tests/thing.rs":   "#[test]\nfn works() {}\n",
		"web/a.spec.ts":    "it('works', () => {});\n",
		"pkg/README_test":  "not go\n",
		"pkg/real_test.go": source("TestReal"),
	})
	r.commit("head")

	got := mustNewTests(t, r.dir, base,
		"pkg/lib.go", "tests/thing.rs", "web/a.spec.ts", "pkg/README_test", "pkg/real_test.go")
	want := []Test{{Scope: "pkg", Name: "TestReal", Path: "pkg/real_test.go", Line: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestTheScanRootMaySitBelowTheRepositoryRoot: the base side is read by blob
// name, which is repository-relative, while the caller's paths are relative to
// the directory it scans. A monorepo run as --dir source is the shape, and
// getting it wrong reports every test as new.
func TestTheScanRootMaySitBelowTheRepositoryRoot(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"source/pkg/a_test.go": source("TestStable")})
	base := r.commit("base")
	r.write(map[string]string{"source/pkg/a_test.go": source("TestStable", "TestAdded")})
	r.commit("head")

	got := mustNewTests(t, filepath.Join(r.dir, "source"), base, "pkg/a_test.go")
	want := []Test{{Scope: "pkg", Name: "TestAdded", Path: "pkg/a_test.go", Line: 8}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// TestNoChangedTestFileAsksGitNothing: a change touching no Go test file has
// no set difference to take, and must not fail over a base it never had to
// read.
func TestNoChangedTestFileAsksGitNothing(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"pkg/lib.go": "package pkg\n"})
	r.commit("base")

	got, err := NewTests(t.Context(), r.dir, "nonexistent-revision", runner.Go, ".", []string{"pkg/lib.go"})
	if err != nil || got != nil {
		t.Errorf("NewTests over no test file = (%+v, %v), want (nil, nil)", got, err)
	}
}

// TestAnUnresolvableMergeBaseIsNamed: there is no "new" without it, and an
// empty result would read as a change that introduced no test at all.
func TestAnUnresolvableMergeBaseIsNamed(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"pkg/a_test.go": source("TestReal")})
	r.commit("head")

	_, err := NewTests(t.Context(), r.dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", runner.Go, ".", []string{"pkg/a_test.go"})
	if !errors.Is(err, ErrNoMergeBase) {
		t.Errorf("NewTests against an unresolvable base = %v, want ErrNoMergeBase", err)
	}
}

// TestAFileThatDoesNotParseFailsRatherThanDeclaringNothing: read as an empty
// declaration set, a parse failure hides every new test in the file at head
// and reports every test in it as new at the base.
func TestAFileThatDoesNotParseFailsRatherThanDeclaringNothing(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"pkg/a_test.go": source("TestReal")})
	base := r.commit("base")
	r.write(map[string]string{"pkg/a_test.go": "package pkg\n\nfunc TestBroken(t *testing.T) {\n"})
	r.commit("head")

	_, err := NewTests(t.Context(), r.dir, base, runner.Go, ".", []string{"pkg/a_test.go"})
	if err == nil || !strings.Contains(err.Error(), "parsing pkg/a_test.go") {
		t.Errorf("NewTests over an unparseable file = %v, want a parse failure naming the file", err)
	}
}

// The result is sorted by package first and by name only within one package,
// so a change touching two packages does not read as sorted by name alone.
func TestNewTestsAreSortedByPackageThenName(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"a/keep.txt": "x\n"})
	base := r.commit("base")
	r.write(map[string]string{
		"b/x_test.go": source("TestB"),
		"a/x_test.go": source("TestA2", "TestA1"),
	})
	r.commit("head")

	got := mustNewTests(t, r.dir, base, "b/x_test.go", "a/x_test.go")
	want := []Test{
		{Scope: "a", Name: "TestA1", Path: "a/x_test.go", Line: 8},
		{Scope: "a", Name: "TestA2", Path: "a/x_test.go", Line: 5},
		{Scope: "b", Name: "TestB", Path: "b/x_test.go", Line: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// testFiles both dedupes and sorts, the way its doc comment promises: a
// caller iterating the result gets one entry per path, in a stable order,
// whatever order the diff listed them in.
func TestGoTestFilesDedupesAndSorts(t *testing.T) {
	got := testFiles(languages[runner.Go], []string{"b/x_test.go", "a/x_test.go", "b/x_test.go", "a/lib.go"})
	want := []string{"a/x_test.go", "b/x_test.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("testFiles = %v, want %v", got, want)
	}
}

// A file exactly at the size bound is read whole, byte for byte — the bound
// is on what is larger than maxFileBytes, not on what reaches it, and the
// limit reader is given one byte more than the bound so a file this size is
// never mistaken for one that has to be rejected.
func TestAFileExactlyAtTheSizeLimitIsReadWhole(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte("a"), maxFileBytes)
	if err := os.WriteFile(filepath.Join(root, "big_test.go"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := readWorkTree(r, "big_test.go")
	if err != nil {
		t.Fatalf("readWorkTree: %v", err)
	}
	if len(got) != maxFileBytes {
		t.Fatalf("read %d byte(s), want the whole %d-byte file, not a truncated copy", len(got), maxFileBytes)
	}
	if !bytes.Equal(got, content) {
		t.Error("the bytes read do not match the file's own content")
	}
}

// A file one byte over the limit is refused by name rather than silently
// truncated to the limit and read as if it were smaller.
func TestAFileOverTheSizeLimitIsRefused(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte("a"), maxFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "big_test.go"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	_, err = readWorkTree(r, "big_test.go")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("readWorkTree over an oversized file = %v, want a refusal naming the bound", err)
	}
}

// isTestName is the compiler's own rule: only a name prefixed "Test" whose
// next rune is not lower-case counts, and a name that does not start with it
// at all is never mistaken for one that does.
func TestIsTestNameRefusesANameWithNoTestPrefix(t *testing.T) {
	for _, name := range []string{"Helper", "BenchmarkFoo", "setup", ""} {
		if isTestName(name) {
			t.Errorf("isTestName(%q) = true, want false: no Test prefix", name)
		}
	}
}

// A file that imports no "testing" package can declare no test — even one
// whose function parameter is spelled `*lydite.T` through an unrelated
// import aliased "lydite", which is what distinguishes testingName's ""
// sentinel from a package name that could ever legitimately match one.
func TestATestShapedFunctionWithNoTestingImportDeclaresNothing(t *testing.T) {
	src := "package pkg\n\n" +
		"import lydite \"example.com/notreal\"\n\n" +
		"func TestDecoy(t *lydite.T) {}\n"
	got, err := declaredTests("pkg", "pkg/a_test.go", []byte(src))
	if err != nil {
		t.Fatalf("declaredTests: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("declaredTests = %+v, want none: the file never imports testing", got)
	}
}

// A test function may spell an explicit empty result list, `func TestX(t
// *testing.T) ()`, which go/ast records as a non-nil, zero-length Results —
// distinct from the nil Results an ordinary declaration gets. Both are zero
// results, and both are a test.
func TestATestWithAnExplicitEmptyResultListIsStillATest(t *testing.T) {
	src := "package pkg\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) () {}\n"
	got, err := declaredTests("pkg", "pkg/a_test.go", []byte(src))
	if err != nil {
		t.Fatalf("declaredTests: %v", err)
	}
	if len(got) != 1 || got[0].Name != "TestX" {
		t.Errorf("declaredTests = %+v, want TestX alone", got)
	}
}

// mustNewTests runs the difference over dir, failing the test on an error.
func mustNewTests(t *testing.T, dir, base string, changed ...string) []Test {
	t.Helper()
	got, err := NewTests(t.Context(), dir, base, runner.Go, ".", changed)
	if err != nil {
		t.Fatalf("NewTests: %v", err)
	}
	return got
}

// source is a test file declaring one func TestXxx per name, the first at line
// 5 and each one three lines below the last.
func source(names ...string) string {
	var b strings.Builder
	b.WriteString("package pkg\n\nimport \"testing\"\n")
	for _, n := range names {
		b.WriteString("\nfunc " + n + "(t *testing.T) {\n}\n")
	}
	return b.String()
}

// repo is a throwaway git repository with a working tree, built commit by
// commit so the difference runs against real blobs rather than against a
// parser's idea of two revisions.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main", ".")
	r.git("config", "user.email", "t@t")
	r.git("config", "user.name", "t")
	r.git("config", "commit.gpgsign", "false")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	res := executil.RunQuiet(r.t.Context(), r.dir, "git", args...)
	if !res.Ok() {
		r.t.Fatalf("git %v: %v\n%s", args, res.Err, res.Stderr)
	}
	return strings.TrimSpace(res.Output)
}

func (r *repo) write(files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *repo) remove(names ...string) {
	r.t.Helper()
	for _, name := range names {
		if err := os.Remove(filepath.Join(r.dir, filepath.FromSlash(name))); err != nil {
			r.t.Fatal(err)
		}
	}
}

// copy lays a materialised fixture tree over the working tree.
func (r *repo) copy(src string) {
	r.t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p) // #nosec G304 -- p comes from a walk of this test's own fixture tree
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		r.t.Fatalf("copying %s: %v", src, err)
	}
	r.write(files)
}

// commit records the working tree and answers the commit it wrote.
func (r *repo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// A Rust test module lives inside the file it tests, so the enumeration reads
// every changed .rs file and not a `tests/` convention: a `#[cfg(test)] mod`
// gaining a test in an ordinary source file is the commonest shape there is,
// and a path predicate would miss all of it.
func TestARustTestAddedInsideAnOrdinarySourceFileIsNew(t *testing.T) {
	const was = `pub fn double(n: i32) -> i32 {
    n * 2
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn doubles() {
        assert_eq!(double(2), 4);
    }
}
`
	r := newRepo(t)
	r.write(map[string]string{"src/lib.rs": was})
	base := r.commit("base")
	r.copy(fixture.Tree(t, filepath.Join("testdata", "nextestprobe")))
	r.commit("head")

	got := mustNewTestsIn(t, r.dir, base, runner.Rust, ".", "src/lib.rs")
	want := []Test{{Scope: ".", Name: "tests::nested::doubles_deeper", Path: "src/lib.rs", Line: 18}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// Every test in a Rust component shares one scope, because one `cargo nextest`
// invocation covers the crate and there is no finer unit lydite should impose.
// A name declared in two files is two declarations under that one scope, which
// is the collision the rerun resolves against run 1's report.
func TestEveryRustTestInAComponentSharesOneScope(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"Cargo.toml": "[package]\nname = \"nextestprobe\"\n"})
	base := r.commit("base")
	r.copy(fixture.Tree(t, filepath.Join("testdata", "nextestprobe")))
	r.commit("head")

	got := mustNewTestsIn(t, r.dir, base, runner.Rust, ".",
		"src/lib.rs", "tests/a.rs", "tests/b.rs", "tests/c.rs")
	want := []Test{
		{Scope: ".", Name: "async_cases::awaits_and_agrees", Path: "tests/c.rs", Line: 3},
		{Scope: ".", Name: "ignored_by_attribute", Path: "tests/c.rs", Line: 10},
		{Scope: ".", Name: "inner::only_in_a", Path: "tests/a.rs", Line: 8},
		{Scope: ".", Name: "shared_name", Path: "tests/a.rs", Line: 2},
		{Scope: ".", Name: "shared_name", Path: "tests/b.rs", Line: 2},
		{Scope: ".", Name: "tests::doubles", Path: "src/lib.rs", Line: 10},
		{Scope: ".", Name: "tests::nested::doubles_deeper", Path: "src/lib.rs", Line: 18},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// A name is not unique across a Rust component's files the way it is within a
// Go package, so the base-vs-head comparison has to key on the file as well:
// a scope-wide "was this name declared before" check would let an unrelated
// file's existing shared_name hide a newly added file's own shared_name test.
func TestASharedNameInANewFileIsNewEvenWhenAnEditedFileAlreadyDeclaresIt(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{
		"Cargo.toml": "[package]\nname = \"nextestprobe\"\n",
		"tests/a.rs": "#[test]\nfn shared_name() {\n    assert!(true);\n}\n",
	})
	base := r.commit("base")
	r.write(map[string]string{
		// Edited, not merely touched: the assertion body changed, and the
		// declaration is still named shared_name — the same test, unmoved.
		"tests/a.rs": "#[test]\nfn shared_name() {\n    assert_eq!(1, 1);\n}\n",
		"tests/b.rs": "#[test]\nfn shared_name() {\n    assert!(true);\n}\n",
	})
	r.commit("head")

	got := mustNewTestsIn(t, r.dir, base, runner.Rust, ".", "tests/a.rs", "tests/b.rs")
	want := []Test{{Scope: ".", Name: "shared_name", Path: "tests/b.rs", Line: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v — an edited tests/a.rs must not hide tests/b.rs's own new shared_name", got, want)
	}
}

// A vitest title is qualified by every describe around it, joined the way
// vitest writes it, and the component is the scope for the same reason Rust's
// is. A title no parser can state is enumerated with its line and no name.
func TestATypeScriptTitleIsQualifiedAndAnUnnameableOneIsCarried(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"package.json": `{"name":"probe"}`})
	base := r.commit("base")
	r.copy(fixture.Tree(t, filepath.Join("testdata", "vitestprobe")))
	r.commit("head")

	got := mustNewTestsIn(t, r.dir, base, runner.TypeScript, ".",
		"src/one.test.ts", "src/two.test.ts")
	want := []Test{
		{Scope: ".", Name: "", Path: "src/one.test.ts", Line: 24, Unreadable: true},
		{Scope: ".", Name: "", Path: "src/one.test.ts", Line: 28, Unreadable: true},
		{Scope: ".", Name: "matches ^a (b) [c] + d$", Path: "src/one.test.ts", Line: 19},
		{Scope: ".", Name: "outer > holds a title one describe deep", Path: "src/one.test.ts", Line: 10},
		{Scope: ".", Name: "outer > inner > holds a title two describes deep", Path: "src/one.test.ts", Line: 5},
		{Scope: ".", Name: "shared title", Path: "src/one.test.ts", Line: 15},
		{Scope: ".", Name: "shared title", Path: "src/two.test.ts", Line: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// An unreadable declaration is in neither set difference: there is no name to
// look up in the base tree. It is reported in every changed test file at HEAD,
// whether or not anything around it is new, because the row's count of what
// went unexamined is a count of what is there now.
func TestAnUnreadableDeclarationIsReportedEvenWhereNothingIsNew(t *testing.T) {
	const src = `import { expect, test } from "vitest";

test.each([[1, 2]])("doubles %i into %i", (n, want) => {
  expect(n * 2).toBe(want);
});

test("names itself", () => {
  expect(1).toBe(1);
});
`
	r := newRepo(t)
	r.write(map[string]string{"src/a.test.ts": src})
	base := r.commit("base")
	r.write(map[string]string{"src/a.test.ts": src + "\n"})
	r.commit("head")

	got := mustNewTestsIn(t, r.dir, base, runner.TypeScript, ".", "src/a.test.ts")
	want := []Test{{Scope: ".", Name: "", Path: "src/a.test.ts", Line: 3, Unreadable: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v: the named test is not new and the unnameable one is neither", got, want)
	}
}

// A Rust attribute the table does not recognise is a test whose names the
// macro decides, and it is counted as unmeasurable rather than assumed inert.
func TestAnUnrecognisedRustAttributeIsUnreadable(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"src/lib.rs": "pub fn double(n: i32) -> i32 {\n    n * 2\n}\n"})
	base := r.commit("base")
	r.copy(fixture.Tree(t, filepath.Join("..", "treesitter", "testdata", "attributeprobe")))
	r.commit("head")

	got := mustNewTestsIn(t, r.dir, base, runner.Rust, ".", "src/lib.rs")
	want := []Test{
		{Scope: ".", Name: "", Path: "src/lib.rs", Line: 18, Unreadable: true},
		{Scope: ".", Name: "tests::doubles", Path: "src/lib.rs", Line: 12},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewTests = %+v, want %+v", got, want)
	}
}

// A component whose language no parser here enumerates is an error and never
// an empty answer: no new tests and no way to look is the difference between a
// gate that passed and one that could not run.
func TestALanguageWithNoParserIsRefused(t *testing.T) {
	if _, err := NewTests(t.Context(), t.TempDir(), "HEAD", "cobol", ".", []string{"a_test.go"}); err == nil {
		t.Error("NewTests answered for a language nothing enumerates")
	}
}

// mustNewTestsIn is the difference over one language's declarations, failing
// the test on an error.
func mustNewTestsIn(t *testing.T, dir, base string, lang runner.Lang, scope string, changed ...string) []Test {
	t.Helper()
	got, err := NewTests(t.Context(), dir, base, lang, scope, changed)
	if err != nil {
		t.Fatalf("NewTests: %v", err)
	}
	return got
}
