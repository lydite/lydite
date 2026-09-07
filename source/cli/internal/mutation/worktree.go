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

	"lydite/lydite/internal/runner"
)

// Tree isolates a component's mutants in a copy of the scan root, with the
// component's own commands run at its directory inside that copy, for a
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
	// Root is the scan root: the tree a worker is a copy of. It is read and
	// never written — every mutant is applied to the source as it stands
	// here, so a worker whose previous mutant was not restored cannot
	// compound.
	Root string
	// Component is the component's directory relative to Root, in slash
	// form, and "." for one rooted at the scan root. Every command runs
	// there inside the worker, and a mutant's own path is relative to it.
	Component string
	// Files are the scan-root-relative paths to copy, in any order.
	//
	// The whole scan root and not the component alone. A component's build
	// routinely reads a file above its own directory — a workspace importing
	// a generated spec, a crate embedding a VERSION with include_str! — and
	// a worker holding the component alone fails to compile every one of
	// those mutants. That reports them unviable, which makes the component
	// `unmeasured`, which does not vote: the gate then examines nothing and
	// the run is green.
	//
	// The scan root and not the enclosing repository, which is a real bound
	// rather than an oversight: git lists the files under the directory it
	// is asked about, and a component's dir cannot escape the scan root. A
	// build reaching above the root lydite was pointed at is one lydite
	// cannot see, and its mutants are unviable for that reason.
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

// dir is the component's own directory in the tree the copy is taken from.
func (t Tree) dir() string { return filepath.Join(t.Root, filepath.FromSlash(t.Component)) }

// Worker copies the tree once and prepares it.
func (t Tree) Worker(ctx context.Context, n int) (Worker, error) {
	dir, err := os.MkdirTemp(t.Parent, fmt.Sprintf("lydite-mutation-%d-", n))
	if err != nil {
		return nil, fmt.Errorf("opening a worker directory for mutation: %w", err)
	}
	w := &treeWorker{tree: t, dir: dir}
	// Before the copy, not after it. populate writes every file the worker
	// holds, and those writes are owed the same containment a mutant's is —
	// a root opened afterwards would confine the one write that was never in
	// doubt and none of the ones that are.
	root, err := os.OpenRoot(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	w.root = root
	if err := w.populate(); err != nil {
		_ = w.Close()
		return nil, err
	}
	if t.Prepare != nil {
		if err := t.Prepare(ctx, filepath.Join(dir, filepath.FromSlash(t.Component))); err != nil {
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

// populate copies the scan root's files into the worker.
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
// instead would change what the build sees.
//
// Every write here goes through the worker's os.Root, for the reason Stage's
// does. The tree being copied is a scanned repository, so it holds symlinks
// nobody vetted: a committed `evil -> /etc` beside a listed `evil/passwd`
// makes the destination path lexically spotless while the open follows the
// link out and truncates a file outside the worker. checkPath cannot see
// that, and says so in its own comment. A link the root refuses is one whose
// target leaves the worker, and the component fails with that path named
// rather than compiling against a tree quietly missing a file its build
// reads.
func (w *treeWorker) copy(rel string) error {
	if err := checkPath(rel); err != nil {
		return err
	}
	from := filepath.Join(w.tree.Root, filepath.FromSlash(rel))
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
	// Slash-separated, because os.Root names are always slash-separated
	// whatever the platform's separator is.
	to := path.Clean(rel)
	if err := w.root.MkdirAll(path.Dir(to), 0o750); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return w.root.Symlink(target, to)
	}
	if !info.Mode().IsRegular() {
		// A socket, a device node or a fifo is not source and cannot be
		// copied meaningfully. Skipped rather than refused: git can track a
		// gitlink, and failing a whole component over one would be a gate
		// firing on ordinary work.
		return nil
	}
	src, err := os.Open(from) // #nosec G304 -- a path git listed under the scan root, checked lexically above; this reads the tree being measured and writes nothing
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := w.root.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil { // [lydite:exclude_from_mutation][both arms close the destination and return nil; they differ only if io.Copy or Close fails between two regular files this function has just opened, which needs a device error no test can provoke]
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
	src, err := os.ReadFile(filepath.Join(w.tree.dir(), filepath.FromSlash(m.Path))) // #nosec G304 -- a component-relative source path, checked above to name a file inside the component
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
	// Joined onto the component's own directory, because a mutant's path is
	// relative to the component and the worker holds the whole repository.
	name := path.Join(w.tree.Component, path.Clean(m.Path))
	if err := w.root.WriteFile(name, mutated, 0o600); err != nil {
		return Staged{}, err
	}
	w.staged, w.original = name, src
	return Staged{
		Dir:   filepath.Join(w.dir, filepath.FromSlash(w.tree.Component)),
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
