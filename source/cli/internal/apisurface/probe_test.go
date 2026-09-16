// Package apisurface compares a Go module's exported API against the
// merge-base.
//
// This file is the measurement ADR 0040's choice of tool rests on: it builds a
// probe module in a real git repository, one head commit per shape of change,
// and records verbatim what `golang.org/x/exp/apidiff` says about each. The
// recorded reports are the evidence, so a change to any of them is a change to
// what the ADR claims and has to be read rather than regenerated.
package apisurface

import (
	"flag"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/exp/apidiff"
	"golang.org/x/tools/go/packages"

	"lydite/lydite/internal/fixture"
)

// probeModule is the probe's module path, which apidiff needs to tell this
// module's packages from anything they import.
const probeModule = "lydite.example/probe"

// update rewrites the recorded reports instead of comparing against them.
var update = flag.Bool("update", false, "rewrite the recorded apidiff reports in testdata")

// shapes are the probe's head commits: one change per shape the ADR reasons
// about, each stating whether apidiff is expected to call it breaking.
var shapes = []struct {
	name         string
	incompatible bool
}{
	{"removed-function", true},
	{"changed-signature", true},
	{"interface-method", true},
	{"widened-field", true},
	{"compatible-addition", false},
	{"excluded-packages", false},
}

// The probe is the only thing that can say what the tool reports, as opposed
// to what its README says it reports. Each shape is compared base-to-head over
// two worktrees of one repository, which is the shape the production check
// runs in.
func TestApidiffReportsEachProbeShape(t *testing.T) {
	repo := newProbe(t)
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			report := repo.compare(t, s.name)
			record(t, s.name+".report.txt", report.String())
			if got := incompatible(report); got != s.incompatible {
				t.Errorf("incompatible = %v, want %v\n%s", got, s.incompatible, report)
			}
		})
	}
}

// incompatible answers whether the report holds a breaking change, which is
// the only part of it a gate may read: a compatible change is ordinary growth.
func incompatible(r apidiff.Report) bool {
	for _, c := range r.Changes {
		if !c.Compatible {
			return true
		}
	}
	return false
}

// probe is the built repository: one base commit and one head commit per shape.
type probe struct {
	dir   string
	base  string
	heads map[string]string
}

func newProbe(t *testing.T) *probe {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "probe@example.invalid"},
		{"config", "user.name", "probe"},
		{"config", "commit.gpgsign", "false"},
	} {
		git(t, dir, args...)
	}
	p := &probe{dir: dir, heads: map[string]string{}}
	p.commit(t, "testdata/probe/base", "base")
	p.base = git(t, dir, "rev-parse", "HEAD")
	for _, s := range shapes {
		// Each shape is its own head off the same base, so no shape sees any
		// other shape's edit.
		git(t, dir, "checkout", "-q", "--detach", p.base)
		p.commit(t, "testdata/probe/"+s.name, s.name)
		p.heads[s.name] = git(t, dir, "rev-parse", "HEAD")
	}
	return p
}

// commit overlays a fixture tree onto the working tree and commits it. Every
// shape replaces a file rather than deleting one, so an overlay is the whole
// edit.
func (p *probe) commit(t *testing.T, dir, message string) {
	t.Helper()
	overlay(t, fixture.Tree(t, dir), p.dir)
	git(t, p.dir, "add", "-A")
	git(t, p.dir, "commit", "-q", "-m", message)
}

// compare loads the base and head trees of one shape and reports what apidiff
// says about the difference.
func (p *probe) compare(t *testing.T, shape string) apidiff.Report {
	t.Helper()
	base := p.worktree(t, p.base)
	head := p.worktree(t, p.heads[shape])
	return apidiff.ModuleChanges(load(t, base), load(t, head))
}

// worktree checks a commit out on its own, which is how the production check
// reaches a tree that is not the one checked out.
func (p *probe) worktree(t *testing.T, sha string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tree")
	git(t, p.dir, "worktree", "add", "-q", "--detach", dir, sha)
	return dir
}

// load reads the module's public surface: every package it declares except the
// ones nothing outside the module can reach.
//
// `internal/` is Go's own public/private boundary, so an internal package's
// exported symbol is not part of any API. Test packages are left out by not
// asking for them at all — an external `_test` package is importable by
// nothing, and its surface changing is not a change any consumer can see.
func load(t *testing.T, dir string) *apidiff.Module {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
		Dir: dir,
		// A tree outside this module must not be read through this module's
		// workspace, and must resolve against its own go.mod.
		Env:   append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local"),
		Tests: false,
	}
	loaded, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("loading %s: %v", dir, err)
	}
	module := &apidiff.Module{Path: probeModule}
	for _, pkg := range loaded {
		if len(pkg.Errors) > 0 {
			t.Fatalf("loading %s: %v", pkg.PkgPath, pkg.Errors)
		}
		if internalPath(pkg.PkgPath) || strings.HasSuffix(pkg.PkgPath, "_test") {
			continue
		}
		module.Packages = append(module.Packages, pkg.Types)
	}
	// packages.Load's order is not specified, and apidiff walks the slice as
	// given, so a report's line order would otherwise vary between runs.
	slices.SortFunc(module.Packages, func(a, b *types.Package) int {
		return strings.Compare(a.Path(), b.Path())
	})
	return module
}

// record compares the report against what is committed, or rewrites it under
// -update.
func record(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- a committed fixture named by this test
	if err != nil {
		t.Fatalf("%v — regenerate with `go test ./internal/apisurface -update`", err)
	}
	if got != string(want) {
		t.Errorf("apidiff's report for %s changed.\n--- got ---\n%s\n--- want ---\n%s\n"+
			"this is the ADR's evidence: read the diff before regenerating with "+
			"`go test ./internal/apisurface -update`", name, got, want)
	}
}

// overlay copies every file under src into dst, creating directories and
// replacing files that are already there.
func overlay(t *testing.T, src, dst string) {
	t.Helper()
	tree := os.DirFS(src)
	err := fs.WalkDir(tree, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("overlaying %s onto %s: %v", src, dst, err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
