package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTreeRestoresTheRealNames(t *testing.T) {
	// A fixture source is committed inert so lydite's own gosec and Semgrep
	// runs cannot fire on it, and is written back under the name the captured
	// report actually states.
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"main.go.txt":  "package main\n",
		"sub/s.go.txt": "package sub\n",
		"report.json":  "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	root := Tree(t, src)
	for name, want := range map[string]string{
		"main.go":  "package main\n",
		"sub/s.go": "package sub\n",
		// A file without the suffix is copied unchanged, so a fixture may hold
		// both a source the scanners must not see and a plain data file.
		"report.json": "{}\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "main.go.txt")); err == nil {
		t.Error("the inert name survived into the materialised tree")
	}
}
