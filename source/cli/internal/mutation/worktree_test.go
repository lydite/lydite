package mutation

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/runner"
)

const tsSrc = "export function less(x: number, y: number) {\n  return x < y\n}\n"

// component writes a small tree and returns the Tree backend over it.
func component(t *testing.T, files map[string]string) Tree {
	t.Helper()
	dir := t.TempDir()
	var names []string
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		names = append(names, rel)
	}
	slices.Sort(names)
	return Tree{
		Dir:    dir,
		Files:  names,
		Parent: t.TempDir(),
		Build:  runner.Invocation{Name: "tsc", Args: []string{"--noEmit"}},
		Suite:  runner.Invocation{Name: "npx", Args: []string{"vitest", "run"}},
	}
}

func lessThan(src string) Mutant {
	return Mutant{
		Path: "src/less.ts", Line: 2, Column: 12, Operator: NegateConditional,
		Offset: strings.Index(src, "<"), Length: 1, Original: "<", Mutated: ">=",
	}
}

func TestAMutantIsWrittenIntoTheWorkerAndNotIntoTheComponent(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc, "package.json": "{}\n"})
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	staged, err := w.Stage(lessThan(tsSrc))
	if err != nil {
		t.Fatal(err)
	}
	if staged.Dir == tree.Dir {
		t.Fatal("the suite would run in the component's own directory")
	}
	if got := contents(t, filepath.Join(staged.Dir, "src", "less.ts")); !strings.Contains(got, "x >= y") {
		t.Errorf("the worker holds %q, want the mutant", got)
	}
	// The component's own tree is what an interrupt would leave behind.
	if got := contents(t, filepath.Join(tree.Dir, "src", "less.ts")); got != tsSrc {
		t.Errorf("the component's own source was edited: %q", got)
	}
	// Everything else came across, or the build fails for a reason that has
	// nothing to do with the mutant.
	if _, err := os.Stat(filepath.Join(staged.Dir, "package.json")); err != nil {
		t.Errorf("the worker is missing a file the component has: %v", err)
	}
	if len(staged.Phases) != 1 {
		t.Errorf("%d phase(s); neither cargo nor a JavaScript runner has a unit cheaper than the component", len(staged.Phases))
	}
}

func TestReleasePutsTheOriginalBack(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	staged, err := w.Stage(lessThan(tsSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Release(); err != nil {
		t.Fatal(err)
	}
	if got := contents(t, filepath.Join(staged.Dir, "src", "less.ts")); got != tsSrc {
		t.Errorf("the worker still holds %q after Release", got)
	}
	// A worker is reused, so the next mutant has to be the only difference
	// between it and the component — one compounded onto another is a mutant
	// nobody generated whose outcome is reported against the operator named
	// in one of them.
	if _, err := w.Stage(lessThan(tsSrc)); err != nil {
		t.Errorf("the worker could not be reused: %v", err)
	}
}

// A worker holds a copy of a scanned repository, so it holds symlinks nobody
// vetted: a committed `evil -> /etc` makes `<worker>/evil/passwd` pass every
// prefix comparison a lexical check can make, and the write then follows the
// link out. checkPath says out loud that it establishes only that a name
// cannot possibly be right; this is where containment is actually owed.
func TestAWriteThatFollowsASymlinkOutOfTheWorkerIsRefused(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "passwd")
	if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	// A symlink the scanned repository committed, pointing out of the tree.
	if err := os.Symlink(outside, filepath.Join(tree.Dir, "evil")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	tree.Files = append(tree.Files, "evil")

	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// A mutant whose path is lexically spotless and resolves outside the
	// worker through that link.
	escape := Mutant{Path: "evil/passwd", Offset: 0, Length: 8, Original: "original", Mutated: "mutated!"}
	if err := os.WriteFile(filepath.Join(tree.Dir, "evil", "passwd"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = w.Stage(escape)
	if err == nil {
		t.Fatal("a write escaped the worker directory")
	}
	// For the right reason: the mutant's path is lexically spotless, so
	// checkPath passes it and Apply matches the bytes. What refuses it is the
	// syscall, and a test that passed because the path looked wrong would
	// prove nothing about containment.
	if !strings.Contains(err.Error(), "path escapes from parent") {
		t.Errorf("Stage failed with %v, want the root to have refused the write", err)
	}
	if got := contents(t, victim); got != "original\n" {
		t.Errorf("the file outside the worker was written: %q", got)
	}
}

func TestAPathThatCannotNameAFileInTheComponentIsRefused(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	for _, path := range []string{"../outside.ts", "/etc/passwd", "", "."} {
		if _, err := w.Stage(Mutant{Path: path, Original: "<", Mutated: ">="}); err == nil {
			t.Errorf("%q was staged", path)
		}
	}
}

// The runner's own preparation runs once per worker rather than once per
// mutant, which is the bound that makes a tree copy affordable at all.
func TestTheRunnerIsPreparedOncePerWorker(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	prepared := 0
	tree.Prepare = func(_ context.Context, dir string) error {
		prepared++
		if dir == tree.Dir {
			t.Error("the runner was prepared in the component's own directory")
		}
		return nil
	}
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	for range 3 {
		if _, err := w.Stage(lessThan(tsSrc)); err != nil {
			t.Fatal(err)
		}
		if err := w.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if prepared != 1 {
		t.Errorf("the runner was prepared %d times for one worker", prepared)
	}
}

// A worker that cannot be prepared is a component that cannot be mutated, and
// says so — rather than every one of its mutants failing to build and being
// reported as evidence about the generator.
func TestAWorkerThatCannotBePreparedIsNotHandedOut(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	tree.Prepare = func(context.Context, string) error { return os.ErrPermission }
	if _, err := tree.Worker(t.Context(), 0); err == nil {
		t.Fatal("a worker whose preparation failed was handed out")
	}
	entries, err := os.ReadDir(tree.Parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed worker left %d directory(ies) behind", len(entries))
	}
}

func TestCloseRemovesTheWorkerDirectory(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := w.Stage(lessThan(tsSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged.Dir); !os.IsNotExist(err) {
		t.Errorf("the worker directory outlived the worker: %v", err)
	}
}

// The listing is git's, in scan-root-relative form; a worker copy needs the
// same paths one level down, and a path outside the component is not this
// component's to copy.
func TestOnlyTheComponentsOwnFilesAreCopied(t *testing.T) {
	files := []string{"source/cli/main.go", "source/web/app.ts", "README.md"}
	if got := ComponentFiles("source/web", files); !slices.Equal(got, []string{"app.ts"}) {
		t.Errorf("ComponentFiles(source/web) = %v, want [app.ts]", got)
	}
	if got := ComponentFiles(".", files); !slices.Equal(got, files) {
		t.Errorf("a component rooted at the scan root got %v, want every file", got)
	}
	// A component whose name is a prefix of another's directory takes none of
	// that one's files: `web` and `web-admin` are separate trees.
	if got := ComponentFiles("web", []string{"web-admin/app.ts"}); len(got) != 0 {
		t.Errorf("ComponentFiles(web) took %v from web-admin", got)
	}
}

func contents(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A path git lists and the filesystem does not is a file deleted since the
// listing. It is not this worker's problem, and refusing to open would fail
// the component over a race with the author's editor.
func TestAFileGitListsAndTheTreeNoLongerHoldsIsSkipped(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	tree.Files = append(tree.Files, "src/deleted.ts")
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatalf("a deleted file failed the worker: %v", err)
	}
	defer func() { _ = w.Close() }()
	staged, err := w.Stage(lessThan(tsSrc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(staged.Dir, "src", "less.ts")); err != nil {
		t.Errorf("the file that is there was not copied: %v", err)
	}
}

// A file the copy cannot reproduce meaningfully — a socket, a fifo, a gitlink —
// is skipped rather than refused. Failing a whole component over one would be
// a gate firing on ordinary work.
func TestAFileThatIsNotSourceIsSkippedRatherThanRefused(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	if err := os.Mkdir(filepath.Join(tree.Dir, "submodule"), 0o750); err != nil {
		t.Fatal(err)
	}
	tree.Files = append(tree.Files, "submodule")
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatalf("an unreproducible entry failed the worker: %v", err)
	}
	_ = w.Close()
}

// A path that cannot name a file inside the component never becomes part of a
// worker either, so a listing carrying one fails before anything is copied.
func TestAWorkerRefusesAListingThatEscapes(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	tree.Files = append(tree.Files, "../outside.ts")
	if _, err := tree.Worker(t.Context(), 0); err == nil {
		t.Error("a listing naming a path outside the component was copied")
	}
}
