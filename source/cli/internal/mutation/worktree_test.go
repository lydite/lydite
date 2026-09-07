package mutation

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/runner"
)

const tsSrc = "export function less(x: number, y: number) {\n  return x < y\n}\n"

// aboveTheComponent is a repository file the component's build reads from
// outside its own directory, which is the shape a worker has to reproduce.
const aboveTheComponent = "docs/openapi.json"

// component writes a small repository with the component one level down, and
// returns the Tree backend over it.
func component(t *testing.T, files map[string]string) Tree {
	t.Helper()
	return componentAt(t, "web", files)
}

// componentAt is the same, with the component at a named directory, so the
// degenerate "." — a component rooted at the scan root, where every join
// collapses — is reachable from a test.
func componentAt(t *testing.T, dir string, files map[string]string) Tree {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{aboveTheComponent}
	write(aboveTheComponent, "{}\n")
	for rel, body := range files {
		write(path.Join(dir, rel), body)
		names = append(names, path.Join(dir, rel))
	}
	slices.Sort(names)
	return Tree{
		Root:      root,
		Component: dir,
		Files:     names,
		Parent:    t.TempDir(),
		Build:     runner.Invocation{Name: "tsc", Args: []string{"--noEmit"}},
		Suite:     runner.Invocation{Name: "npx", Args: []string{"vitest", "run"}},
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
	if staged.Dir == tree.dir() {
		t.Fatal("the suite would run in the component's own directory")
	}
	if got := contents(t, filepath.Join(staged.Dir, "src", "less.ts")); !strings.Contains(got, "x >= y") {
		t.Errorf("the worker holds %q, want the mutant", got)
	}
	// The component's own tree is what an interrupt would leave behind.
	if got := contents(t, filepath.Join(tree.dir(), "src", "less.ts")); got != tsSrc {
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
	if err := os.Symlink(outside, filepath.Join(tree.dir(), "evil")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	tree.Files = append(tree.Files, "web/evil")

	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// A mutant whose path is lexically spotless and resolves outside the
	// worker through that link.
	escape := Mutant{Path: "evil/passwd", Offset: 0, Length: 8, Original: "original", Mutated: "mutated!"}
	if err := os.WriteFile(filepath.Join(tree.dir(), "evil", "passwd"), []byte("original\n"), 0o600); err != nil {
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
		if dir == tree.dir() {
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

// A worker holds the repository and not the component alone, and the
// component's commands run at its own directory inside the copy.
//
// A component's build routinely reads a file above itself — a workspace
// importing a generated spec, a crate embedding a VERSION with include_str! —
// and a copy narrowed to the component fails to compile every one of its
// mutants. That reports them unviable, which makes the component
// `unmeasured`, which does not vote: the gate examines nothing and the run is
// green, which is the one failure mode a mutation gate must not have.
func TestTheWorkerHoldsWhatTheComponentReadsFromAboveItself(t *testing.T) {
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
	// The commands run at the component, so a mutant's own path resolves the
	// way its compiler resolves it.
	if got := contents(t, filepath.Join(staged.Dir, "src", "less.ts")); !strings.Contains(got, "x >= y") {
		t.Errorf("the component directory inside the worker holds %q, want the mutant", got)
	}
	above := filepath.Join(staged.Dir, "..", filepath.FromSlash(aboveTheComponent))
	if _, err := os.Stat(above); err != nil {
		t.Errorf("the worker is missing %s, which the component's build reads: %v", aboveTheComponent, err)
	}
}

// A worker's own writes are confined, not merely checked, and the copy is
// where that is hardest to see: checkPath is lexical, so a listing naming a
// path beneath a committed symlink is spotless, and an unconfined open would
// follow the link and truncate a file outside the worker. This asserts the
// syscall refused it and the file outside is untouched — a test that passed
// because the path looked wrong would prove nothing about containment.
func TestACopyThatFollowsASymlinkOutOfTheWorkerIsRefused(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "passwd")
	if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	if err := os.Symlink(outside, filepath.Join(tree.dir(), "evil")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	// The listing an index carrying a symlink and an entry beneath it yields,
	// in the order git reports it: the link first, then the path through it.
	tree.Files = append(tree.Files, "web/evil", "web/evil/passwd")

	w, err := tree.Worker(t.Context(), 0)
	if err == nil {
		_ = w.Close()
		t.Fatal("a listing that writes through a symlink out of the worker was copied")
	}
	if got := contents(t, victim); got != "original\n" {
		t.Errorf("the file outside the worker was written: %q", got)
	}
}

// Every join in this package collapses when a component is rooted at the scan
// root, so the case is the one most likely to be got wrong and the least
// likely to be noticed: the commands run at the worker itself, and a mutant's
// path is already the path inside it.
func TestAComponentRootedAtTheScanRoot(t *testing.T) {
	tree := componentAt(t, ".", map[string]string{"src/less.ts": tsSrc})
	var prepared string
	tree.Prepare = func(_ context.Context, dir string) error {
		prepared = dir
		return nil
	}
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	if tree.dir() != tree.Root {
		t.Errorf("dir() is %q, want the scan root %q", tree.dir(), tree.Root)
	}
	staged, err := w.Stage(lessThan(tsSrc))
	if err != nil {
		t.Fatal(err)
	}
	if prepared != staged.Dir {
		t.Errorf("the runner was prepared in %q and the suite runs in %q", prepared, staged.Dir)
	}
	if got := contents(t, filepath.Join(staged.Dir, "src", "less.ts")); !strings.Contains(got, "x >= y") {
		t.Errorf("the worker holds %q, want the mutant", got)
	}
	// The file above the component is above the scan root too, so it is in no
	// listing and the worker cannot hold it.
	if _, err := os.Stat(filepath.Join(staged.Dir, filepath.FromSlash(aboveTheComponent))); err != nil {
		t.Errorf("the worker is missing %s: %v", aboveTheComponent, err)
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
	tree.Files = append(tree.Files, "web/src/deleted.ts")
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
	if err := os.Mkdir(filepath.Join(tree.dir(), "submodule"), 0o750); err != nil {
		t.Fatal(err)
	}
	tree.Files = append(tree.Files, "web/submodule")
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatalf("an unreproducible entry failed the worker: %v", err)
	}
	_ = w.Close()
}

// A path that cannot name a file inside the repository never becomes part of a
// worker either, so a listing carrying one fails before anything is copied.
func TestAWorkerRefusesAListingThatEscapes(t *testing.T) {
	tree := component(t, map[string]string{"src/less.ts": tsSrc})
	tree.Files = append(tree.Files, "../outside.ts")
	if _, err := tree.Worker(t.Context(), 0); err == nil {
		t.Error("a listing naming a path outside the component was copied")
	}
}

// A worker is a copy, and a copy that produced empty files is a component
// whose every suite fails for a reason nothing in the report names. Nothing
// else here reads a copied file back: the mutated one is written by Stage and
// the original is put back from the component's own tree, so both are bytes
// this package supplied rather than bytes the copy carried.
func TestAWorkerHoldsTheComponentsOwnBytes(t *testing.T) {
	const manifest = "{\n  \"name\": \"less\"\n}\n"
	tree := component(t, map[string]string{"src/less.ts": tsSrc, "package.json": manifest})
	w, err := tree.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	staged, err := w.Stage(lessThan(tsSrc))
	if err != nil {
		t.Fatal(err)
	}
	if got := contents(t, filepath.Join(staged.Dir, "package.json")); got != manifest {
		t.Errorf("the copied manifest is %q, want the component's own %q", got, manifest)
	}
	info, err := os.Stat(filepath.Join(staged.Dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0 {
		t.Error("the copy is unreadable, so the runner cannot read what it was given")
	}
}

// Close is total. It is what Worker calls when preparing the tree fails, and
// a worker that never opened a root still holds a directory somebody has to
// remove — so the root is closed if there is one and the directory goes
// either way.
func TestAWorkerWithNoRootIsStillClosed(t *testing.T) {
	dir := t.TempDir() + "/worker"
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	w := &treeWorker{dir: dir}
	if err := w.Close(); err != nil {
		t.Fatalf("closing a worker that never opened a root: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the worker directory outlived a worker with no root: %v", err)
	}
}
