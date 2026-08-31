package config

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinDirectoriesAreExcluded keeps lydite's own .lydite/config.yml honest.
//
// The pin directories hold real package manifests — package.json, Cargo.toml,
// go.mod — so detection treats each one as a package to lint, a crate to audit,
// or a module to scan unless it is excluded. Getting that wrong is quiet rather
// than loud: CI's self-scan would start linting a manifest that exists only for
// Dependabot to read, and the first sign would be a confusing failure in an
// unrelated PR. Adding a pin directory without excluding it fails here instead.
//
// Every language's list is checked separately, not just AllExcludes(): only the
// initial detect.Ecosystems pass uses the merged list, while each language's own
// discovery pass (rust.Check, typescript.Check, golang.Check) reads only its own.
func TestPinDirectoriesAreExcluded(t *testing.T) {
	// Three levels up, not two: the Go module lives in cli/, while .lydite/config.yml
	// sits at the repository root because that is the scan root CI passes to
	// --dir. Two levels reaches cli/, where Load finds no config and returns an
	// empty one — which reads as "no pin directory is excluded" and fails here.
	const repoRoot = "../../.."

	var pinDirs []string
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if name := d.Name(); name == ".git" || name == "node_modules" {
			return fs.SkipDir
		}
		if strings.HasSuffix(d.Name(), "-pin") {
			pinDirs = append(pinDirs, d.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo: %v", err)
	}
	if len(pinDirs) == 0 {
		t.Fatal("found no *-pin directories; this test is no longer testing anything")
	}

	cfg, err := Load(repoRoot)
	if err != nil {
		t.Fatalf("loading lydite's own .lydite/config.yml: %v", err)
	}

	for _, lang := range []struct {
		name    string
		exclude []string
	}{
		{"rust", cfg.Rust.Exclude},
		{"typescript", cfg.TypeScript.Exclude},
		{"go", cfg.Go.Exclude},
	} {
		excluded := map[string]bool{}
		for _, e := range lang.exclude {
			excluded[e] = true
		}
		for _, pin := range pinDirs {
			if !excluded[pin] {
				t.Errorf("%s.exclude in .lydite/config.yml is missing %q — CI's self-scan would scan it", lang.name, pin)
			}
		}
	}
}
