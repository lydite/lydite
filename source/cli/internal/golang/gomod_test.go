package golang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStripModComment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\trequire x v1.0.0 // indirect", "\trequire x v1.0.0 "},
		// A line that is only a comment leaves nothing, which is what keeps a
		// commented-out require from being read as one.
		{"// require x v1.0.0", ""},
		{"require x v1.0.0", "require x v1.0.0"},
		// A single slash is not a comment introducer.
		{"replace x => ./local", "replace x => ./local"},
	}
	for _, tc := range cases {
		if got := stripModComment(tc.in); got != tc.want {
			t.Errorf("stripModComment(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGoModIgnoresAReplaceOntoALocalPath(t *testing.T) {
	// A replace with no version on its right names no module the build
	// resolves by version, and there is nothing to bump. Recording it would
	// anchor an advisory to a line whose edit cannot clear it.
	dir := t.TempDir()
	manifest := "module example.com/m\n" + // 1
		"go 1.24\n" + // 2
		"replace a.example/x => ../local\n" + // 3
		"replace b.example/y => c.example/z v1.0.0\n" // 4
	if err := os.WriteFile(filepath.Join(dir, goModFile), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	mod := readGoMod(dir)
	if got := mod.Line("../local"); got != 0 {
		t.Errorf("a local path was recorded at line %d, want none", got)
	}
	if got := mod.Line("a.example/x"); got != 0 {
		t.Errorf("the replaced module was recorded at line %d, want none — the replacement is what the build uses", got)
	}
	// The right-hand side with a version is the module the build resolves.
	if got := mod.Line("c.example/z"); got != 4 {
		t.Errorf("the replacement is at line %d, want 4", got)
	}
}

func TestGoModIgnoresAReplaceWithNoModuleBeforeTheArrow(t *testing.T) {
	// A replace names the module it replaces before the arrow. A line opening
	// with `=>` names none, so there is no entry to file — and reading the
	// right-hand side out of one anyway would record a module under a line
	// whose edit cannot clear an advisory against it.
	dir := t.TempDir()
	manifest := "module example.com/m\n" + // 1
		"go 1.24\n" + // 2
		"replace (\n" + // 3
		"\t=> c.example/z v1.0.0\n" + // 4
		")\n" // 5
	if err := os.WriteFile(filepath.Join(dir, goModFile), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readGoMod(dir).Line("c.example/z"); got != 0 {
		t.Errorf("a replace with no module before the arrow recorded one at line %d, want none", got)
	}
}

// A replace's right-hand side is what the build resolves, and its version is
// what a dependency-delta comparison needs — not only the line the advisory
// gate anchors to.
func TestGoModDependenciesIncludesAReplacesVersion(t *testing.T) {
	manifest := "module example.com/m\n" +
		"go 1.24\n" +
		"require a.example/x v1.2.0\n" +
		"replace b.example/y => c.example/z v1.0.0\n"
	got := GoModDependencies([]byte(manifest))
	if want := []string{"v1.2.0"}; !slices.Equal(got["a.example/x"], want) {
		t.Errorf("a.example/x = %v, want %v", got["a.example/x"], want)
	}
	if want := []string{"v1.0.0"}; !slices.Equal(got["c.example/z"], want) {
		t.Errorf("c.example/z = %v, want %v", got["c.example/z"], want)
	}
	if _, ok := got["b.example/y"]; ok {
		t.Error("the replaced module names no version the build resolves and should not appear")
	}
}

// A malformed line's own number is what a reader has to find and fix, not an
// off-by-one neighbour.
func TestGoSumDependenciesNamesTheMalformedLinesOwnNumber(t *testing.T) {
	content := "github.com/spf13/cobra v1.10.1 h1:aaaa=\n" + // line 1, well-formed
		"github.com/spf13/cobra v1.10.1\n" // line 2, missing its hash
	_, _, err := GoSumDependencies([]byte(content))
	if err == nil {
		t.Fatal("a two-field line extracted a set")
	}
	if got, want := err.Error(), "go.sum line 2: "; !strings.HasPrefix(got, want) {
		t.Errorf("error = %q, want a prefix naming line 2, got %q", got, want)
	}
}

// The module-zip line's hash is the origin a same-version comparison needs,
// whichever order the two lines for one version arrive in — and a module
// naming only its go.mod line, with no zip line at all, still gets that
// line's hash rather than none.
func TestGoSumDependenciesPrefersTheZipHashOverTheGoModHashInEitherOrder(t *testing.T) {
	zipFirst := "example.com/a v1.0.0 h1:zip=\n" +
		"example.com/a v1.0.0/go.mod h1:gomod=\n"
	_, origins, err := GoSumDependencies([]byte(zipFirst))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := origins[[2]string{"example.com/a", "v1.0.0"}], "h1:zip="; got != want {
		t.Errorf("zip line first: origin = %q, want %q", got, want)
	}

	goModFirst := "example.com/b v1.0.0/go.mod h1:gomod=\n" +
		"example.com/b v1.0.0 h1:zip=\n"
	_, origins, err = GoSumDependencies([]byte(goModFirst))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := origins[[2]string{"example.com/b", "v1.0.0"}], "h1:zip="; got != want {
		t.Errorf("go.mod line first: origin = %q, want %q", got, want)
	}

	goModOnly := "example.com/c v1.0.0/go.mod h1:gomod-only=\n"
	_, origins, err = GoSumDependencies([]byte(goModOnly))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := origins[[2]string{"example.com/c", "v1.0.0"}], "h1:gomod-only="; got != want {
		t.Errorf("go.mod line alone: origin = %q, want %q", got, want)
	}
}

func TestGoModKeepsTheFirstGoDirective(t *testing.T) {
	// A toolchain advisory anchors to the `go` directive, and a manifest
	// naming one twice keeps the first — the line a bump is written on.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, goModFile),
		[]byte("module m\ngo 1.24\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readGoMod(dir).Line("stdlib"); got != 2 {
		t.Errorf("the go directive is at line %d, want 2", got)
	}
}
