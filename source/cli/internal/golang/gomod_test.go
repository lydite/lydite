package golang

import (
	"os"
	"path/filepath"
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
