// Package nodedeps decides how a JavaScript workspace's dependencies get
// installed, so the coverage gate and the test runner agree on the answer.
//
// A JavaScript suite cannot run at all without an install: a fresh checkout
// has no node_modules and every import fails before a single test is
// collected. Go and Rust have no equivalent step — their toolchains fetch
// what a build needs on the way past — which is why this exists for one
// language and not three.
//
// The rule lives here rather than in either caller because both ask the same
// question of the same tree. Two copies would answer it the same way until
// one of them learned about a package manager the other had not, at which
// point coverage and the suite would install differently in the same
// repository and only one of them would be wrong.
package nodedeps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"lydite/lydite/internal/executil"
)

// lockfiles maps each recognised lockfile to the package manager it
// identifies.
var lockfiles = map[string]string{
	"package-lock.json": "npm",
	"yarn.lock":         "yarn",
	"pnpm-lock.yaml":    "pnpm",
}

// Manager inspects root for exactly one recognised lockfile.
//
// More than one — usually a stale file nobody deleted — is ambiguous, and
// reports false rather than guessing a priority order. Installing with the
// wrong manager writes a lockfile the repository does not use, so the caller
// skipping the install is the smaller failure, and `typescript.install` in
// .lydite/config.yml is how a repository in that state says what it means.
func Manager(root string) (string, bool) {
	var found []string
	for file, manager := range lockfiles {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			found = append(found, manager)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// HasLockfile reports whether dir holds any recognised lockfile, ambiguous or
// not — the question a caller walking for workspace roots asks, where
// presence is what matters and not which single manager it names.
func HasLockfile(dir string) bool {
	for file := range lockfiles {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return true
		}
	}
	return false
}

// WorkspaceRoot is the directory an install for dir runs in: dir itself, or
// the nearest ancestor of it holding a recognised lockfile, and false when
// neither does.
//
// A package of a workspace declares its dependencies nowhere — the lockfile
// that resolves them sits at the root above it — so a walk is what turns a
// declared component directory into the directory an install is possible in.
// Without it a component at packages/ui in a repository whose only
// pnpm-lock.yaml is at the root installs nothing at all, and its suite then
// fails at import naming the tests rather than the absent dependencies.
//
// scanRoot bounds the walk: it is the repository lydite was pointed at, and a
// lockfile above it belongs to a tree this run was never asked about. A dir
// outside scanRoot is its own bound, so the walk can never climb past what the
// caller named either way.
//
// The nearest directory holding *any* lockfile ends the walk, ambiguous or
// not. A root carrying two of them is still the root its packages share, and
// continuing past it would install from a grandparent whose lockfile resolves
// different versions than the ones this package sits under — so ambiguity
// resolves to nothing here exactly as it does in Manager.
func WorkspaceRoot(dir, scanRoot string) (string, bool) {
	dir = filepath.Clean(dir)
	bound := dir
	if within(dir, scanRoot) {
		bound = filepath.Clean(scanRoot)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if HasLockfile(d) {
			if _, ok := Manager(d); !ok {
				return "", false
			}
			return d, true
		}
		if d == bound || filepath.Dir(d) == d {
			return "", false
		}
	}
}

// Declared is the package manager a workspace's package.json names in its
// `packageManager` field, and the exact version it pins.
type Declared struct {
	// Name is the manager, one of Managers().
	Name string
	// Version is the exact version the field pins ("8.15.4"), with any
	// integrity hash removed.
	Version string
	// Hash is the integrity hash the field carries after `+`
	// ("sha512.<hex>"), verbatim, or "" when it carries none.
	Hash string
	// Source is the package.json the field was read from, for messages.
	Source string
}

// exactVersion is the only version form `packageManager` admits: Corepack
// installs exactly what it names, so a range there is not a pin.
var exactVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// PackageManager reads the `packageManager` field of root's package.json —
// the field Corepack itself reads — and reports false with no error when the
// field is absent, which leaves Manager's lockfile detection as the answer.
//
// root is a directory WorkspaceRoot resolved: the manifest declaring the
// manager for a whole workspace sits beside the lockfile it resolves, so this
// reads that one file and never walks for another.
//
// The field only ever adds a version to a manager the lockfile already
// identified; it never overrides one. A root whose lockfiles are ambiguous
// stays refused exactly as Manager refuses it, and a field naming a manager
// other than the one its lockfile identifies is an error rather than a choice
// between them — a package.json saying yarn beside a pnpm-lock.yaml is a
// misconfigured repository, and installing with either manager writes a
// lockfile the other half of it does not use. A field that is present but
// cannot be read as `<name>@<exact version>[+<hash>]` is an error too: it is
// a pin the repository meant, and falling back to an unpinned manager would
// ignore it without saying so.
func PackageManager(root string) (Declared, bool, error) {
	source := filepath.Join(root, "package.json")
	// #nosec G304 -- root is a workspace root resolved from the repository's own component declaration, not a scanned file's contents
	data, err := os.ReadFile(source)
	if err != nil {
		return Declared{}, false, nil
	}
	var manifest struct {
		PackageManager *string `json:"packageManager"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Declared{}, false, fmt.Errorf("%s: %w", source, err)
	}
	if manifest.PackageManager == nil {
		return Declared{}, false, nil
	}
	field := *manifest.PackageManager
	name, pin, found := strings.Cut(field, "@")
	version, hash, _ := strings.Cut(pin, "+")
	if !found || name == "" || !exactVersion.MatchString(version) {
		return Declared{}, false, fmt.Errorf("%s: packageManager %q is not <name>@<exact version>[+<hash>]", source, field)
	}
	if !slices.Contains(Managers(), name) {
		return Declared{}, false, fmt.Errorf("%s: packageManager names %q, which is not one of %s",
			source, name, strings.Join(Managers(), ", "))
	}
	detected, ok := Manager(root)
	if !ok {
		return Declared{}, false, nil
	}
	if detected != name {
		return Declared{}, false, fmt.Errorf("%s: packageManager names %s, but the lockfile beside it is %s's", source, name, detected)
	}
	return Declared{Name: name, Version: version, Hash: hash, Source: source}, true, nil
}

// within reports whether dir is root or lies below it.
func within(dir, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), dir)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// PackageVersion reads an installed package's own version out of the tree an
// install produced, and reports false when there is nothing to read.
//
// It reads node_modules rather than the lockfile deliberately. The question is
// what *ran*, not what should have resolved: a lockfile states an intention,
// and a tree installed before it changed, or one lydite skipped installing,
// answers that question wrongly in the direction that matters. It is also one
// code path for npm, yarn and pnpm, where the lockfile is three formats with
// their own schemas — and pnpm's symlink into .pnpm resolves on the way past.
//
// False is an ordinary answer and not a failure. A Yarn PnP workspace has no
// node_modules at all, and a caller that cannot identify a package must say so
// rather than guess.
func PackageVersion(root, pkg string) (string, bool) {
	// #nosec G304 -- nosemgrep: go.lang.security.audit.dangerous-file-read.dangerous-file-read -- root is the component directory from the repository's own declaration, and pkg is one of the fixed package names internal/runner names for each runner; neither reaches here from a scanned file's contents
	data, err := os.ReadFile(filepath.Join(root, "node_modules", filepath.FromSlash(pkg), "package.json"))
	if err != nil {
		return "", false
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Version == "" {
		return "", false
	}
	return manifest.Version, true
}

// Managers lists every recognised package manager, sorted, for a message
// that tells the reader what would have been detected.
func Managers() []string {
	out := make([]string, 0, len(lockfiles))
	for _, m := range lockfiles {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Command is one step of an install.
type Command struct {
	// Argv is the program and its arguments, passed as argv with no shell —
	// except the override, which is a shell invocation by construction.
	Argv []string
}

// managers names the package managers Commands can detect and internal/toolchain
// provisions — the argv[0]s Install checks for on PATH before running them, so
// an absent one fails with a name and a reason rather than exec's bare
// "executable file not found in $PATH".
var managers = map[string]bool{"npm": true, "yarn": true, "pnpm": true}

// managerOnPath reports an error naming manager when it cannot be found on the
// PATH env composes — the PATH the child Install is about to run under, which
// is not necessarily this process's own.
//
// npm ships with every Node lydite provisions, so it is always there; yarn
// and pnpm are what internal/toolchain provisions separately when
// packageManager pins one. A manager still missing at this point means that
// provisioning did not happen or did not reach this component's environment,
// and the reader needs to know which manager and why — not exec's bare
// "executable file not found in $PATH", which names neither.
func managerOnPath(manager string, env []string) error {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			dirs = filepath.SplitList(v)
		}
	}
	for _, d := range dirs {
		info, err := os.Stat(filepath.Join(d, manager)) // #nosec G703 -- d comes from this process's own PATH or a PATH this package composed from its own provisioning output; manager is one of the fixed names in the managers table, not user input
		if err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return nil
		}
	}
	return fmt.Errorf("%s: not on PATH — lydite could not provision it; see the warning above, or install it manually", manager)
}

// Commands is what installing at root takes, in order, or nothing when no
// single manager could be identified.
//
// override, from typescript.install, replaces detection entirely and is run
// through a shell. A free-form user-authored command legitimately needs shell
// semantics (&&, env expansion), unlike lydite's own hardcoded tool
// invocations — and it is the only way to express a Corepack-pinned or
// otherwise nonstandard flow that no lockfile can imply.
//
// Every detected form is a *frozen* install: `npm ci`, `yarn --immutable`,
// `pnpm --frozen-lockfile`. An install that may rewrite the lockfile would
// have lydite silently change what the repository resolves to, and then
// measure and gate the result.
func Commands(root, override string) []Command {
	if override != "" {
		return []Command{{Argv: []string{"sh", "-c", override}}}
	}
	manager, ok := Manager(root)
	if !ok {
		return nil
	}
	switch manager {
	case "npm":
		return []Command{{Argv: []string{"npm", "ci"}}}
	case "yarn":
		return []Command{{Argv: []string{"yarn", "install", "--immutable"}}}
	case "pnpm":
		return []Command{{Argv: []string{"pnpm", "install", "--frozen-lockfile"}}}
	default:
		return nil
	}
}

// Install runs the install dir's dependencies need, writing each command's
// output to out, and reports the first command that failed or nil when there
// was nothing to do.
//
// It runs in the workspace root WorkspaceRoot resolves for dir, bounded by
// scanRoot — a frozen install from the root is what installs the package, and
// running a package manager in a directory holding no lockfile installs
// nothing.
//
// An override replaces what runs, never where: it runs at the root
// WorkspaceRoot resolves exactly as a detected install would, coalesced with
// every sibling resolving that root. Keyed on dir instead, every component
// carrying the override would be its own key and run the same install
// concurrently over one shared node_modules, lockfile and store. Only when no
// root resolves — an ambiguous multi-lockfile root, or no lockfile at all —
// does the override run in dir itself, keyed there and shared with no other
// component; the ambiguous root is one of the cases the override exists for,
// and there is no single root there to coalesce against.
//
// One root is installed once: every component resolving it shares the single
// install the first of them runs, and one arriving while that runs waits for
// it rather than starting its own. That install runs under one component's
// environment, so a second component naming a different one is an error
// rather than a silent share of whichever declaration got there first — see
// installedUnder.
//
// Whether a failure is fatal is the caller's to decide, and the two callers
// answer differently: the coverage gate omits a package it cannot measure,
// while a test run that proceeds after a failed install reports import errors
// naming the tests rather than the missing dependencies.
func Install(ctx context.Context, dir, scanRoot, override string, env []string, out io.Writer) error {
	root := filepath.Clean(dir)
	if override == "" {
		var ok bool
		if root, ok = WorkspaceRoot(dir, scanRoot); !ok {
			return nil
		}
	} else if r, ok := WorkspaceRoot(dir, scanRoot); ok {
		root = r
	}
	if v, done := installed.Load(root); done {
		return installedUnder(root, v.([]string), env)
	}
	unlock := lockRoot(root)
	defer unlock()
	// Re-checked under the lock: the install this one waited for is the one it
	// was about to do, over the same node_modules tree.
	if v, done := installed.Load(root); done {
		return installedUnder(root, v.([]string), env)
	}
	for _, cmd := range Commands(root, override) {
		if managers[cmd.Argv[0]] {
			if err := managerOnPath(cmd.Argv[0], env); err != nil {
				return err
			}
		}
		// #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- every argv is built above from a fixed set, except the override, which comes from the target repo's own .lydite/config.yml and is authored by whoever configured lydite for that repo
		res := executil.RunOutput(ctx, root, env, out, cmd.Argv[0], cmd.Argv[1:]...)
		if !res.Ok() {
			// Only success is recorded, so the next component resolving this
			// root installs again: a root whose install failed has no tree to
			// share, and skipping would hand it a second failure it cannot
			// explain.
			return fmt.Errorf("%s: %w", strings.Join(cmd.Argv, " "), res.Err)
		}
	}
	installed.Store(root, env)
	return nil
}

// installedUnder reports the environment a root's shared install actually ran
// under, and an error when this call names a different one.
//
// A workspace root's install is one process, so it runs under exactly one
// environment — the first component that reaches it. A second component
// declaring a different one is not a case coalescing can honour silently:
// installing under either component's environment could write dependencies,
// tokens or a registry the other did not ask for into the tree it also
// imports from, and picking one at random by scheduling order is worse than
// saying so.
func installedUnder(root string, installed, env []string) error {
	if envEqual(installed, env) {
		return nil
	}
	return fmt.Errorf("%s: installed under a different environment than this component declares — "+
		"components sharing a workspace root must declare the same env, since the install runs once for all of them", root)
}

// envEqual compares two `KEY=value` environments regardless of order: what a
// component declares and what its toolchain adds can compose in either
// order, and two installs asking for the same environment must not be told
// they differ because of it.
func envEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// installLocks serialises installs of one workspace root within this process,
// and installed names the roots an install has already completed for, with
// the environment that install ran under.
//
// In-process only, and that is the whole of what it claims: two lydite
// processes installing the same workspace still race. What it closes is the
// case this process creates for itself by preparing components concurrently —
// a frozen install run from inside a workspace package installs the *whole*
// workspace, so every component resolving one root writes one node_modules
// tree, and internal/scheduler locks a component's own declared directory and
// the ports it publishes, never a root above them all.
var (
	installLocks sync.Map
	installed    sync.Map
)

func lockRoot(root string) func() {
	v, _ := installLocks.LoadOrStore(root, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
