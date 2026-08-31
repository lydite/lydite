package orphan_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/orphan"
)

// The gate is the guard on a declared list, so the case it exists for is a
// source file in a directory nobody declared — including one holding no
// manifest at all, which is what detection could never see.
func TestAnUndeclaredDirectoryIsOrphaned(t *testing.T) {
	root := repo(t, map[string]string{
		"cli/main.go":     "package main\n",
		"scripts/seed.ts": "export const seed = 1\n",
	})
	res := find(t, root, component.File{Components: []component.Component{{Name: "cli", Dir: "cli"}}})
	if want := []string{"scripts/seed.ts"}; !equal(res.Orphans, want) {
		t.Errorf("orphans = %v, want %v", res.Orphans, want)
	}
}

// Only a language lydite has a runner for. A file no component could ever
// claim is not a question the gate can act on either way, and demanding an
// exclude for one is paperwork that trains people to stop reading the list.
func TestOnlySourceOfALanguageLyditeRunsCounts(t *testing.T) {
	root := repo(t, map[string]string{
		"cli/main.go":        "package main\n",
		"README.md":          "# hi\n",
		"LICENSE":            "MIT\n",
		"VERSION":            "1.0.0\n",
		"Makefile":           "all:\n",
		"docs/openapi.json":  "{}\n",
		"scripts/install.sh": "#!/bin/sh\n",
		"assets/logo.svg":    "<svg/>\n",
		".github/ci.yml":     "on: push\n",
	})
	res := find(t, root, component.File{Components: []component.Component{{Name: "cli", Dir: "cli"}}})
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none — none of these is code a component could test", res.Orphans)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", res.Scanned)
	}
}

// Every language with a runner is seen, or the gate is blind to a whole
// ecosystem while reporting a clean pass.
func TestEveryRunnableLanguageIsSeen(t *testing.T) {
	root := repo(t, map[string]string{
		"a/thing.go":  "package a\n",
		"b/thing.rs":  "fn main() {}\n",
		"c/thing.ts":  "export const x = 1\n",
		"d/thing.tsx": "export const y = 1\n",
		"e/thing.mjs": "export const z = 1\n",
	})
	res := find(t, root, component.File{})
	if res.Scanned != 5 {
		t.Errorf("scanned = %d, want 5: %v", res.Scanned, res.Orphans)
	}
}

// A repository declaring nothing is the failure at its purest: every source
// file is under no component, and a run that passed would be the declared
// list failing open exactly as designed.
func TestNoComponentsOrphansEverything(t *testing.T) {
	root := repo(t, map[string]string{"cli/main.go": "package main\n"})
	res := find(t, root, component.File{})
	if want := []string{"cli/main.go"}; !equal(res.Orphans, want) {
		t.Errorf("orphans = %v, want %v", res.Orphans, want)
	}
}

// A component rooted at the scan root covers the whole tree. "." is not a
// prefix any cleaned path carries, so this is the one dir that needs saying.
func TestAComponentAtTheRootCoversEverything(t *testing.T) {
	root := repo(t, map[string]string{"a/thing.go": "package a\n", "b/thing.rs": "fn main() {}\n"})
	res := find(t, root, component.File{Components: []component.Component{{Name: "all", Dir: "."}}})
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none", res.Orphans)
	}
}

// A component dir must not cover a sibling whose name it is a prefix of.
func TestComponentDirMatchesWholeSegments(t *testing.T) {
	root := repo(t, map[string]string{"web/a.ts": "export const a = 1\n", "website/b.ts": "export const b = 1\n"})
	res := find(t, root, component.File{Components: []component.Component{{Name: "web", Dir: "web"}}})
	if want := []string{"website/b.ts"}; !equal(res.Orphans, want) {
		t.Errorf("orphans = %v, want %v", res.Orphans, want)
	}
}

// Excludes are anchored, like every other path pattern lydite declares. A
// bare directory name covers the path of that name and nothing inside it,
// which is stricter than gitignore and is the point: a pattern that silently
// covers more than it appears to is what the gate exists to prevent.
func TestExcludesAreAnchoredAndReportedWhenTheyCoverNothing(t *testing.T) {
	root := repo(t, map[string]string{"scripts/seed.ts": "export const s = 1\n"})

	res := find(t, root, component.File{Excludes: []string{"scripts"}})
	if want := []string{"scripts/seed.ts"}; !equal(res.Orphans, want) {
		t.Errorf("orphans = %v, want %v — a bare directory name is not a subtree", res.Orphans, want)
	}
	if want := []string{"scripts"}; !equal(res.UnusedExcludes, want) {
		t.Errorf("unused = %v, want %v", res.UnusedExcludes, want)
	}

	res = find(t, root, component.File{Excludes: []string{"scripts/**"}})
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none", res.Orphans)
	}
	if len(res.UnusedExcludes) != 0 {
		t.Errorf("unused = %v, want none", res.UnusedExcludes)
	}
}

// Build output and installed dependencies are not the repository's source,
// and a gate reporting node_modules as untested is one that gets switched
// off on its first run. git already holds that judgement in .gitignore.
func TestIgnoredFilesAreNotSource(t *testing.T) {
	root := repo(t, map[string]string{
		".gitignore":                    "node_modules/\ntarget/\n",
		"web/node_modules/dep/index.js": "module.exports = 1\n",
		"rust/target/debug/build.rs":    "fn main() {}\n",
	})
	res := find(t, root, component.File{})
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none", res.Orphans)
	}
}

// A file written but not yet staged is the moment the author can most
// cheaply act on the answer, so the gate sees it. It is not ignored, only
// unstaged, which is a different thing from build output.
func TestAnUntrackedFileIsStillSource(t *testing.T) {
	root := repo(t, map[string]string{"cli/main.go": "package main\n"})
	if err := os.WriteFile(filepath.Join(root, "loose.go"), []byte("package loose\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := find(t, root, component.File{Components: []component.Component{{Name: "cli", Dir: "cli"}}})
	if want := []string{"loose.go"}; !equal(res.Orphans, want) {
		t.Errorf("orphans = %v, want %v", res.Orphans, want)
	}
}

// Without a repository the set of files the repository contains is not a
// question that can be answered, and the caller renders that as a gate that
// did not run rather than one that passed.
func TestOutsideAGitRepositoryIsItsOwnError(t *testing.T) {
	if _, err := orphan.Find(context.Background(), t.TempDir(), component.File{}); !errors.Is(err, orphan.ErrNoRepository) {
		t.Errorf("err = %v, want ErrNoRepository", err)
	}
}

func find(t *testing.T, root string, f component.File) orphan.Result {
	t.Helper()
	res, err := orphan.Find(context.Background(), root, f)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	return res
}

// repo writes the files and makes the tree a git repository, because that is
// what the gate reads the file list from.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--quiet")
	run("add", "-A")
	return root
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// An exclude covers files whether or not a component also covers them, so a
// pattern whose every match happens to sit inside a component's dir has not
// "covered no file". Warning about it would put a line on stderr on every run
// of a correct declaration, which is how a diagnostic teaches people to
// ignore it.
func TestAnExcludeMatchingInsideAComponentIsNotUnused(t *testing.T) {
	root := repo(t, map[string]string{"cli/api.gen.go": "package cli\n", "cli/main.go": "package cli\n"})
	res := find(t, root, component.File{
		Components: []component.Component{{Name: "cli", Dir: "cli"}},
		Excludes:   []string{"**/*.gen.go"},
	})
	if len(res.UnusedExcludes) != 0 {
		t.Errorf("unused = %v, want none — the pattern matches cli/api.gen.go", res.UnusedExcludes)
	}
}

// Two excludes that overlap are both used. Stopping at the first match would
// report the second as covering nothing, on a declaration that is correct.
func TestOverlappingExcludesAreBothUsed(t *testing.T) {
	root := repo(t, map[string]string{"generated/client.ts": "export const c = 1\n"})
	res := find(t, root, component.File{Excludes: []string{"generated/**", "generated/client.ts"}})
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none", res.Orphans)
	}
	if len(res.UnusedExcludes) != 0 {
		t.Errorf("unused = %v, want none — both patterns match the file", res.UnusedExcludes)
	}
}

// git's diagnostics go to stderr, so an error built from stdout is an empty
// string and the reader is left with a bare exit status and no reason.
func TestAGitFailureIsReportedWithItsMessage(t *testing.T) {
	root := repo(t, map[string]string{"a.go": "package a\n"})
	// A corrupt index, rather than a broken .git: `git rev-parse` never
	// reads the index, so it still reports a work tree and the run reaches
	// the failure this is about instead of ErrNoRepository.
	if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("not an index"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := orphan.Find(context.Background(), root, component.File{})
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, orphan.ErrNoRepository) {
		t.Fatalf("err = %v, want the ls-files failure rather than a missing repository", err)
	}
	if !strings.Contains(err.Error(), "index") {
		t.Errorf("err = %v, want git's own message naming the bad index, not a bare exit status", err)
	}
}
