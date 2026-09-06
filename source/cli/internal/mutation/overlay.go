package mutation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/runner"
)

// Go isolates a Go component's mutants with `go build -overlay`.
//
// In-process mutation was never available for Go: it compiles to a native test
// binary and has no bytecode layer to rewrite in a live process, so the real
// choice was an overlay against copying the tree per mutant. The overlay wins
// and is why Mutant.Apply returns bytes rather than writing them — nothing
// here edits the component's own tree, so an interrupt cannot leave mutated
// source in the repository lydite is measuring.
//
// The suite is never given -count=1, and that is load-bearing rather than an
// omission. An overlay changes the build hash of the mutated package and of
// its dependents, and of nothing else, so exactly the right set of tests
// re-runs and everything else is served from the test cache: mutation gets
// incremental test selection without lydite implementing any. Measured over
// five distinct mutants of one leaf package in this repository, 1.67s each
// against 79s with the cache disabled.
type Go struct {
	// Dir is the component's directory, where every command runs. Go needs
	// no worker directory: an overlay names the mutated file wherever it is
	// written, so the compiler reads it from a temporary file while every
	// other path resolves in the component's own tree.
	Dir string
	// Build is the component's build-only invocation, and Suite its plain
	// one. Both come from the component's declared runner, so what mutation
	// compiles and what it runs are the same suite the coverage gate and the
	// test matrix run.
	Build, Suite runner.Invocation
}

// Worker opens a directory for one concurrency slot's overlay files.
//
// They are a few hundred bytes each, so the number of them is a property of
// the interface rather than a cost: a Go mutant needs no tree copy, and the
// same Backend covers the languages that do.
func (g Go) Worker(n int) (Worker, error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("lydite-mutation-%d-", n))
	if err != nil {
		return nil, fmt.Errorf("opening a worker directory for mutation: %w", err)
	}
	return &goWorker{backend: g, dir: dir}, nil
}

type goWorker struct {
	backend Go
	dir     string
	// staged is what Stage wrote, so Release removes what is there rather
	// than what a second copy of the naming rule says should be.
	staged []string
}

// overlay is the document `go build -overlay` reads: the file the compiler
// would have read, mapped to the file it reads instead.
type overlay struct {
	Replace map[string]string
}

// Stage writes the mutated source and the overlay naming it.
//
// The source is read from the component's tree at this moment rather than from
// anything the generator kept, so a file that changed between generation and
// execution is refused by Apply instead of being spliced at an offset that now
// holds something else.
func (w *goWorker) Stage(m Mutant) (Staged, error) {
	if err := checkPath(m.Path); err != nil {
		return Staged{}, err
	}
	original := filepath.Join(w.backend.Dir, filepath.FromSlash(m.Path))
	src, err := os.ReadFile(original) // #nosec G304 -- the path is a component-relative source file, checked above to name one inside the component
	if err != nil {
		return Staged{}, err
	}
	mutated, err := m.Apply(src)
	if err != nil {
		return Staged{}, err
	}
	// A fixed name, carrying nothing of the mutant's own path. The go command
	// reads the replacement's *content* and reports the path the overlay
	// replaced, so the name is lydite's to choose — and a name derived from
	// the mutant would put a string from the scanned repository into a path
	// this writes to, which is a traversal surface bought for a filename
	// nobody reads. The build tag and package clause that decide how it is
	// compiled are in the content, unchanged apart from the mutation.
	source := filepath.Join(w.dir, "mutant.go")
	w.staged = []string{source}
	if err := os.WriteFile(source, mutated, 0o600); err != nil { // #nosec G703 -- the path is a constant basename inside a directory this process created with os.MkdirTemp; nothing from the scanned repository reaches it
		return Staged{}, err
	}
	// Absolute on both sides. The go command resolves a relative entry
	// against its own working directory, which is the component's here and
	// the worker's nowhere.
	origAbs, err := filepath.Abs(original)
	if err != nil {
		return Staged{}, err
	}
	doc, err := json.Marshal(overlay{Replace: map[string]string{origAbs: source}})
	if err != nil {
		return Staged{}, err
	}
	file := filepath.Join(w.dir, "overlay.json")
	w.staged = []string{source, file}
	if err := os.WriteFile(file, doc, 0o600); err != nil {
		return Staged{}, err
	}

	flag := "-overlay=" + file
	staged := Staged{
		Dir:    w.backend.Dir,
		Build:  withFlag(w.backend.Build, flag),
		Phases: []runner.Invocation{withFlag(w.backend.Suite, flag)},
	}
	// The mutant's own package first, and the whole closure only if it
	// survives there. A kill in its own package is final, and this is the
	// common outcome in a tested repository; a survivor pays nothing for the
	// second run, which reads the first's cached result for the package the
	// two share.
	if unit, ok := narrow(staged.Phases[0], goPackage(m.Path)); ok {
		staged.Phases = []runner.Invocation{unit, staged.Phases[0]}
	}
	return staged, nil
}

// Release removes what Stage put in place, so a worker whose next Stage fails
// cannot hand the compiler the previous mutant's overlay.
func (w *goWorker) Release() error {
	var errs []error
	for _, path := range w.staged {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	w.staged = nil
	return errors.Join(errs...)
}

func (w *goWorker) Close() error { return os.RemoveAll(w.dir) }

// withFlag splices a build flag in immediately after the subcommand, which is
// where the go command's own documentation puts one and the only position that
// is before the package list whatever the component declared.
func withFlag(inv runner.Invocation, flag string) runner.Invocation {
	out := inv
	if len(inv.Args) == 0 {
		out.Args = []string{flag}
		return out
	}
	out.Args = append([]string{inv.Args[0], flag}, inv.Args[1:]...)
	return out
}

// goPackage is the import path pattern of the package holding a file, relative
// to the component directory.
func goPackage(file string) string {
	dir := path.Dir(file)
	if dir == "." {
		return "."
	}
	return "./" + dir
}

// narrow replaces the package patterns in an invocation with one package, and
// reports whether that produced a different command.
//
// It rewrites only arguments that are package patterns and leaves every other
// one alone, so a flag and its separately written value survive intact. What
// makes the rule safe rather than merely usually right is that the narrowed
// run is never authoritative: a mutant it fails to kill is run again against
// the unnarrowed phase, so the worst a misread argument can cost is the second
// run this exists to avoid — never a wrong verdict.
//
// Everything after `-args` belongs to the test binary rather than to the go
// command, so the scan stops there.
func narrow(inv runner.Invocation, pkg string) (runner.Invocation, bool) {
	out := inv
	out.Args = make([]string, len(inv.Args))
	copy(out.Args, inv.Args)
	replaced := false
	for i, arg := range out.Args {
		if arg == "-args" || arg == "--args" {
			break
		}
		if !isGoPackagePattern(arg) {
			continue
		}
		out.Args[i] = pkg
		replaced = true
	}
	if !replaced {
		out.Args = append(out.Args, pkg)
	}
	if strings.Join(out.Args, "\x00") == strings.Join(inv.Args, "\x00") {
		return runner.Invocation{}, false
	}
	return out, true
}

// isGoPackagePattern reports whether an argument names packages rather than
// being a flag or a flag's value.
//
// The forms the go command documents: a relative path, `all`, and anything
// carrying the `...` wildcard. An import path with no punctuation at all is
// deliberately not one of them — it is indistinguishable from a flag value,
// and reading it as a package is the direction that costs a wrong command
// rather than a second run.
func isGoPackagePattern(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "-") {
		return false
	}
	return arg == "." || arg == ".." || arg == "all" ||
		strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") ||
		strings.Contains(arg, "...")
}
