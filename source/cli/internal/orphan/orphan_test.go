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

// Only a language lydite recognises as source. Prose, a licence, a build
// file and a data document are not code any component claims, and demanding
// an exclude for one is paperwork that trains people to stop reading the list.
func TestOnlySourceOfALanguageLyditeRecognisesCounts(t *testing.T) {
	root := repo(t, map[string]string{
		"cli/main.go":       "package main\n",
		"README.md":         "# hi\n",
		"LICENSE":           "MIT\n",
		"VERSION":           "1.0.0\n",
		"Makefile":          "all:\n",
		"docs/openapi.json": "{}\n",
		"assets/logo.svg":   "<svg/>\n",
		".github/ci.yml":    "on: push\n",
	})
	res := find(t, root, component.File{Components: []component.Component{{Name: "cli", Dir: "cli"}}})
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none — none of these is code a component could test", res.Orphans)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", res.Scanned)
	}
}

// A component declaring a raw command claims the files under its directory
// whatever they are written in, so a script it covers is nobody's orphan —
// and the suite that command runs is the only thing lydite could have asked
// about it either way.
func TestACommandComponentClaimsAScriptItCovers(t *testing.T) {
	root := repo(t, map[string]string{
		"tools/build.py":   "print('x')\n",
		"tools/release.sh": "#!/bin/sh\n",
		"tools/lib.bash":   "true\n",
	})
	f := component.File{Components: []component.Component{
		{Name: "tools", Dir: "tools", Command: []string{"make", "check"}},
	}}
	res := find(t, root, f)
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none — the component covers all three", res.Orphans)
	}
	if res.Scanned != 3 {
		t.Errorf("scanned = %d, want 3 — a .py, a .sh and a .bash are source", res.Scanned)
	}
}

// A script under no component and no exclude is the gate's own case: code
// tested by nobody, in a repository whose every run still reports green. The
// author clears it by declaring the component that runs it or by writing the
// exclude that says nothing does.
func TestAnUnclaimedScriptIsOrphaned(t *testing.T) {
	root := repo(t, map[string]string{
		"cli/main.go":          "package main\n",
		"scripts/release.py":   "print('x')\n",
		"scripts/install.sh":   "#!/bin/sh\n",
		"scripts/helpers.bash": "true\n",
	})
	f := component.File{Components: []component.Component{{Name: "cli", Dir: "cli"}}}
	res := find(t, root, f)
	want := []string{"scripts/helpers.bash", "scripts/install.sh", "scripts/release.py"}
	if !equal(res.Orphans, want) {
		t.Errorf("orphans = %v, want %v", res.Orphans, want)
	}

	// And an exclude clears them, exactly as it clears any other source file.
	f.Excludes = []string{"scripts/**"}
	if res := find(t, root, f); len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none once the exclude covers them", res.Orphans)
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
		// A declared component, so the tree is not empty: an empty one is
		// its own state and would not exercise the skipping this is about.
		"cli/main.go": "package main\n",
	})
	res := find(t, root, component.File{Components: []component.Component{{Name: "cli", Dir: "cli"}}})
	if len(res.Orphans) != 0 {
		t.Errorf("orphans = %v, want none", res.Orphans)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1 — only cli/main.go is the repository's source", res.Scanned)
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

// A scan root that is itself ignored is inside a work tree and lists nothing,
// exiting zero. Reported as its own state rather than as a clean pass: a gate
// that examined no file at all must never read as one that examined the
// repository and found everything declared.
func TestAnIgnoredScanRootIsNotACleanPass(t *testing.T) {
	root := repo(t, map[string]string{
		".gitignore":    "vendored/\n",
		"a.go":          "package a\n",
		"vendored/b.go": "package b\n",
	})
	_, err := orphan.Find(context.Background(), filepath.Join(root, "vendored"), component.File{})
	if !errors.Is(err, orphan.ErrNoFiles) {
		t.Errorf("err = %v, want ErrNoFiles", err)
	}
}

// A repository holding no code in any language lydite runs is the same
// state, reached honestly rather than by a misdirected --dir.
func TestARepositoryWithNoSourceIsUnmeasurable(t *testing.T) {
	root := repo(t, map[string]string{"README.md": "# docs\n", "LICENSE": "MIT\n"})
	if _, err := orphan.Find(context.Background(), root, component.File{}); !errors.Is(err, orphan.ErrNoFiles) {
		t.Errorf("err = %v, want ErrNoFiles", err)
	}
}

// A language the repository has disabled for scanning is still code a
// component ought to test, and the gate deliberately does not read those
// flags. Honouring them would let a repository drop a whole language out of
// the gate by changing what its linter runs on.
func TestScanningOptOutsDoNotNarrowTheGate(t *testing.T) {
	root := repo(t, map[string]string{"web/app.ts": "export const a = 1\n"})
	res := find(t, root, component.File{})
	if want := []string{"web/app.ts"}; !equal(res.Orphans, want) {
		t.Errorf("orphans = %v, want %v", res.Orphans, want)
	}
}
