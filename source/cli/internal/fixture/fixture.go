// Package fixture materialises a captured source tree for a test to read.
//
// A scanner parser is tested against a report captured from the real tool, and
// a report names files: the line a claim fired on is read out of the tree to
// identify it. So the sources the report names have to be there to be read.
//
// They cannot be committed under their own extensions. Two of lydite's own
// gates walk this repository — gosec descends into testdata, and Semgrep scans
// every file it is given — and a fixture that reproduces a finding is by
// construction code those gates fire on. Committing it makes lydite's own scan
// permanently red for code nothing builds, ships or imports, and the only
// alternatives are to weaken the repository's own scan or to blunt the fixture
// until it no longer reproduces what the tool saw.
//
// So each file is committed with a `.txt` suffix, which nothing treats as
// source, and Tree writes it back under its real name for the one test that
// reads it. It exists once rather than per package for the reason
// finding.Source does: a second copy would strip the suffix by a different
// rule, and the two would agree until one of them learned something.
package fixture

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// suffix is what marks a committed fixture file as inert.
const suffix = ".txt"

// Tree copies dir into a temporary directory, restoring each file's real name,
// and answers where it wrote it.
//
// A file without the suffix is copied unchanged, so a fixture may hold both a
// source the scanners must not see and a plain data file.
//
// Reads go through os.DirFS, which resolves every path within the fixture
// directory at the syscall rather than by joining strings a walk handed back.
// It is the rule finding.Source already follows, and it leaves no window
// between resolving a path and reading it.
func Tree(t *testing.T, dir string) string {
	t.Helper() // [lydite:exclude_from_mutation][removing it moves a failure's reported line from the caller to this file and changes nothing else, so no assertion can tell the two apart; dropping the call to rewrite past the mutant would make every fixture failure point here instead of at the test that asked for it]
	root := t.TempDir()
	src := os.DirFS(dir)
	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(path, suffix)))
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("materialising %s: %v", dir, err)
	}
	return root
}
