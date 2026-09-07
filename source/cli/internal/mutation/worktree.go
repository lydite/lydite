package mutation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/runner"
)

// Tree isolates a component's mutants in a copy of its own directory, for a
// toolchain with no equivalent of Go's build overlay.
//
// Cargo and every JavaScript runner read source from the filesystem and take
// no instruction about reading one file from somewhere else, so the mutated
// file has to exist as a file. Writing it into the component's own tree is
// rejected: it is faster than either alternative and an interrupt leaves
// mutated source in the repository lydite is measuring, which is a tree
// somebody then commits.
//
// One directory per concurrency slot and never one per mutant. A tree copy —
// and, for a JavaScript workspace, a dependency install — is not affordable
// per mutant, so a worker is reused with the mutated file restored between
// them.
type Tree struct {
	// Dir is the component's directory, absolute. It is read and never
	// written: every mutant is applied to the source as it stands here, so a
	// worker whose previous mutant was not restored cannot compound.
	Dir string
	// Files are the component-relative paths to copy, in any order.
	//
	// The caller supplies git's own list — tracked, plus untracked files git
	// is not ignoring — which is the list internal/orphan already reads.
	// Reading it from git rather than walking the tree is what keeps
	// node_modules, target, dist and .git out of the copy without lydite
	// holding a second copy of a judgement .gitignore already states, and the
	// copy that drifts is the one that starts copying half a gigabyte of
	// build output per slot.
	Files []string
	// Build and Suite are the component's own build-only and plain
	// invocations.
	Build, Suite runner.Invocation
	// Prepare puts in place what the runner needs in a fresh worker — a
	// JavaScript workspace's node_modules, a pinned cargo subcommand — or is
	// nil for a runner that needs nothing. It runs once per worker rather
	// than once per mutant, which is the bound that makes the copy
	// affordable at all.
	Prepare func(ctx context.Context, dir string) error
	// Parent is where worker directories are created; the OS temporary
	// directory when empty.
	//
	// It is deliberately not under the component: a worker inside the tree
	// being measured is a directory the component's own `./...` would
	// compile, its own coverage would report, and the orphan gate would see
	// as source under no component.
	Parent string
}

// Worker copies the tree once and prepares it.
func (t Tree) Worker(ctx context.Context, n int) (Worker, error) {
	dir, err := os.MkdirTemp(t.Parent, fmt.Sprintf("lydite-mutation-%d-", n))
	if err != nil {
		return nil, fmt.Errorf("opening a worker directory for mutation: %w", err)
	}
	w := &treeWorker{tree: t, dir: dir}
	if err := w.populate(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	w.root = root
	if t.Prepare != nil {
		if err := t.Prepare(ctx, dir); err != nil {
			_ = w.Close()
			return nil, fmt.Errorf("preparing the worker directory: %w", err)
		}
	}
	return w, nil
}

type treeWorker struct {
	tree Tree
	dir  string
	// root confines every write this worker makes to its own directory.
	//
	// Confined rather than checked, and the difference is the whole of it. A
	// worker holds a copy of a scanned repository, so it holds symlinks
	// nobody vetted: a committed `evil -> /etc` makes `<worker>/evil/passwd`
	// pass every prefix comparison a lexical check can make, and the write
	// then follows the link out. os.Root refuses that at the syscall, and
	// leaves no window between resolving a path and writing to it.
	// checkPath's own comment says it establishes only that a name cannot
	// possibly be right; this is where containment is actually owed.
	root *os.Root
	// staged is the mutant currently presented, and the original bytes to put
	// back. Empty between mutants.
	staged   string
	original []byte
}

// populate copies the component's files into the worker.
func (w *treeWorker) populate() error {
	for _, rel := range w.tree.Files {
		if err := w.copy(rel); err != nil {
			return fmt.Errorf("copying %s into the worker directory: %w", rel, err)
		}
	}
	return nil
}

// copy reproduces one file, keeping its mode and reproducing a symlink as a
// symlink rather than as its target's contents.
//
// A symlink is copied because the tree may need it — a JavaScript workspace's
// bin shims, a Rust crate linked in from a sibling — and copying the target
// instead would change what the build sees. It is safe to have one here
// precisely because every later write goes through os.Root, which refuses to
// follow one out of the worker.
func (w *treeWorker) copy(rel string) error {
	if err := checkPath(rel); err != nil {
		return err
	}
	from := filepath.Join(w.tree.Dir, filepath.FromSlash(rel))
	info, err := os.Lstat(from)
	if err != nil {
		// A path git lists and the filesystem does not is a file deleted
		// since the listing. It is not this worker's problem, and refusing to
		// open would fail the component over a race with the author's editor.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	to := filepath.Join(w.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(target, to)
	}
	if !info.Mode().IsRegular() {
		// A socket, a device node or a fifo is not source and cannot be
		// copied meaningfully. Skipped rather than refused: git can track a
		// gitlink, and failing a whole component over one would be a gate
		// firing on ordinary work.
		return nil
	}
	src, err := os.Open(from) // #nosec G304 -- a component-relative source path, checked above to name a file inside the component
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()) // #nosec G304 -- a path inside the worker directory this process just created
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil { //lydite:equivalent both arms close the destination and return nil; they differ only if io.Copy or Close fails between two regular files this function has just opened, which needs a device error no test can provoke
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// Stage writes the mutated source into the worker.
//
// The source is read from the component's own tree at this moment rather than
// from the worker's copy, so a file that changed between generation and
// execution is refused by Apply instead of being spliced at an offset that now
// holds something else — and so a worker whose previous mutant was somehow not
// restored cannot compound one mutant onto another.
func (w *treeWorker) Stage(m Mutant) (Staged, error) {
	if err := checkPath(m.Path); err != nil {
		return Staged{}, err
	}
	src, err := os.ReadFile(filepath.Join(w.tree.Dir, filepath.FromSlash(m.Path))) // #nosec G304 -- a component-relative source path, checked above to name a file inside the component
	if err != nil {
		return Staged{}, err
	}
	mutated, err := m.Apply(src)
	if err != nil {
		return Staged{}, err
	}
	// Through the root, so a path that leaves the worker — by traversal, or
	// by following a symlink the scanned repository committed — is refused at
	// the syscall rather than by a comparison of strings.
	name := path.Clean(m.Path)
	if err := w.root.WriteFile(name, mutated, 0o600); err != nil {
		return Staged{}, err
	}
	w.staged, w.original = name, src
	return Staged{
		Dir:   w.dir,
		Build: w.tree.Build,
		// One phase. Neither cargo nor a JavaScript runner has a unit that is
		// both cheaper than the component and derivable from a file path the
		// way a Go package directory is: a crate needs its manifest read, and
		// a JavaScript test file is related to the source it exercises by
		// convention rather than by structure. A second phase that narrowed
		// wrongly would cost the run it exists to save.
		Phases: []runner.Invocation{w.tree.Suite},
	}, nil
}

// Release puts the original source back, so the next mutant is the only
// difference between the worker and the component's own tree.
func (w *treeWorker) Release() error {
	if w.staged == "" {
		return nil
	}
	name, original := w.staged, w.original
	w.staged, w.original = "", nil
	return w.root.WriteFile(name, original, 0o600)
}

func (w *treeWorker) Close() error {
	var errs []error
	if w.root != nil {
		errs = append(errs, w.root.Close())
	}
	errs = append(errs, os.RemoveAll(w.dir))
	return errors.Join(errs...)
}

// ComponentFiles narrows a repository-wide file listing to one component's own
// files, as paths relative to it.
//
// The listing is git's, in scan-root-relative form, which is what
// internal/orphan and internal/gitdiff already work in; a worker copy needs
// the same paths one level down. A path outside the component is not this
// component's to copy.
func ComponentFiles(dir string, files []string) []string {
	prefix := path.Clean(dir)
	out := make([]string, 0, len(files))
	for _, f := range files {
		if prefix == "." {
			out = append(out, f)
			continue
		}
		if rel, ok := strings.CutPrefix(f, prefix+"/"); ok {
			out = append(out, rel)
		}
	}
	return out
}
