package flaky

import (
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
	got, err := NewTests(t.Context(), r.dir, base, changed.All)
	if err != nil {
		t.Fatalf("NewTests: %v", err)
	}
	want := []Test{
		{Package: ".", Name: "TestDeterministicNew", Path: "probe_test.go", Line: 23},
		{Package: ".", Name: "TestFlakyNew", Path: "probe_test.go", Line: 32},
		{Package: ".", Name: "TestNewSubtests", Path: "probe_test.go", Line: 44},
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
	want := []Test{{Package: "pkg", Name: "TestNewName", Path: "pkg/a_test.go", Line: 5}}
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
	want := []Test{{Package: "new", Name: "TestStable", Path: "new/a_test.go", Line: 5}}
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
		{Package: "pkg", Name: "TestOne", Path: "pkg/added_test.go", Line: 5},
		{Package: "pkg", Name: "TestTwo", Path: "pkg/added_test.go", Line: 8},
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
	want := []Test{{Package: "pkg", Name: "TestReal", Path: "pkg/a_test.go", Line: 7}}
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
	want := []Test{{Package: "pkg", Name: "TestTagged", Path: "pkg/a_test.go", Line: 7}}
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
	want := []Test{{Package: "pkg", Name: "TestReal", Path: "pkg/real_test.go", Line: 5}}
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
	want := []Test{{Package: "pkg", Name: "TestAdded", Path: "pkg/a_test.go", Line: 8}}
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

	got, err := NewTests(t.Context(), r.dir, "nonexistent-revision", []string{"pkg/lib.go"})
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

	_, err := NewTests(t.Context(), r.dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", []string{"pkg/a_test.go"})
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

	_, err := NewTests(t.Context(), r.dir, base, []string{"pkg/a_test.go"})
	if err == nil || !strings.Contains(err.Error(), "parsing pkg/a_test.go") {
		t.Errorf("NewTests over an unparseable file = %v, want a parse failure naming the file", err)
	}
}

// mustNewTests runs the difference over dir, failing the test on an error.
func mustNewTests(t *testing.T, dir, base string, changed ...string) []Test {
	t.Helper()
	got, err := NewTests(t.Context(), dir, base, changed)
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
