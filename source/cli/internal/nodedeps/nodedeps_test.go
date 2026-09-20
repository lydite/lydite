package nodedeps

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestManager(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lockfiles []string
		wantMgr   string
		wantOK    bool
	}{
		{"npm", []string{"package-lock.json"}, "npm", true},
		{"yarn", []string{"yarn.lock"}, "yarn", true},
		{"pnpm", []string{"pnpm-lock.yaml"}, "pnpm", true},
		{"none", nil, "", false},
		// Installing with the wrong manager writes a lockfile the repository
		// does not use, so no answer beats a guessed priority order.
		{"ambiguous", []string{"package-lock.json", "yarn.lock"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, lf := range tc.lockfiles {
				write(t, dir, lf)
			}
			mgr, ok := Manager(dir)
			if ok != tc.wantOK || mgr != tc.wantMgr {
				t.Fatalf("Manager = (%q, %v), want (%q, %v)", mgr, ok, tc.wantMgr, tc.wantOK)
			}
		})
	}
}

// A workspace root is found by presence, not by an unambiguous answer: a root
// carrying two lockfiles is still the root its packages share.
func TestHasLockfileIsPresenceNotResolution(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package-lock.json")
	write(t, dir, "yarn.lock")
	if !HasLockfile(dir) {
		t.Error("a root with two lockfiles still has one")
	}
	if _, ok := Manager(dir); ok {
		t.Error("two lockfiles must not resolve to a manager")
	}
	if got := Commands(dir, ""); got != nil {
		t.Errorf("Commands = %v, want nothing to run", got)
	}
}

// An install that may rewrite the lockfile would have lydite silently change
// what the repository resolves to, and then measure and gate the result.
func TestEveryDetectedInstallIsFrozen(t *testing.T) {
	frozen := map[string]string{
		"package-lock.json": "ci",
		"yarn.lock":         "--immutable",
		"pnpm-lock.yaml":    "--frozen-lockfile",
	}
	for lockfile, flag := range frozen {
		dir := t.TempDir()
		write(t, dir, lockfile)
		cmds := Commands(dir, "")
		if len(cmds) == 0 {
			t.Fatalf("%s resolved to no install", lockfile)
		}
		last := strings.Join(cmds[len(cmds)-1].Argv, " ")
		if !strings.Contains(last, flag) {
			t.Errorf("%s installs with %q, want %q", lockfile, last, flag)
		}
	}
}

// An override is the only way to express a Corepack-pinned or otherwise
// nonstandard flow, so it replaces detection rather than adding to it.
func TestOverrideReplacesDetection(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package-lock.json")
	cmds := Commands(dir, "corepack enable && yarn install --immutable")
	if len(cmds) != 1 || cmds[0].Argv[0] != "sh" {
		t.Fatalf("Commands = %v, want a single shell command", cmds)
	}
	if cmds[0].Argv[2] != "corepack enable && yarn install --immutable" {
		t.Errorf("Commands = %v, want the override verbatim", cmds)
	}
}

// An override applies to a root no lockfile identifies — that is most of why
// it exists.
// corepack may already be enabled, or absent on an older Node; treating it
// as fatal would fail a repository over a step that had nothing to do.
func TestOnlyCorepackIsOptional(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "yarn.lock")
	cmds := Commands(dir, "")
	if len(cmds) != 2 {
		t.Fatalf("Commands = %v, want corepack then yarn", cmds)
	}
	if !cmds[0].Optional {
		t.Error("corepack enable must be optional")
	}
	if cmds[1].Optional {
		t.Error("the install itself must not be optional")
	}
}

func TestOverrideAppliesWithoutALockfile(t *testing.T) {
	if got := Commands(t.TempDir(), "make deps"); len(got) != 1 {
		t.Errorf("Commands = %v, want the override to run anyway", got)
	}
}

func write(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// mkdir makes a directory below root and returns it.
func mkdir(t *testing.T, root string, parts ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A package of a workspace declares its dependencies nowhere: the lockfile
// resolving them is at the root above it, and an install run in the package
// installs nothing at all.
func TestTheWorkspaceRootIsWalkedUpTo(t *testing.T) {
	for _, tc := range []struct {
		name string
		// lockfiles maps a directory under the scan root — "." for the root
		// itself — to the lockfiles it holds.
		lockfiles map[string][]string
		wantDir   string
		wantOK    bool
	}{
		{
			name:      "the component's own lockfile",
			lockfiles: map[string][]string{"packages/ui": {"pnpm-lock.yaml"}},
			wantDir:   "packages/ui",
			wantOK:    true,
		},
		{
			name:      "the root above it",
			lockfiles: map[string][]string{".": {"pnpm-lock.yaml"}},
			wantDir:   ".",
			wantOK:    true,
		},
		{
			// The nearer root is the one whose lockfile resolves what this
			// package sits under.
			name:      "the nearest of two",
			lockfiles: map[string][]string{".": {"pnpm-lock.yaml"}, "packages": {"yarn.lock"}},
			wantDir:   "packages",
			wantOK:    true,
		},
		{
			name:      "no lockfile anywhere",
			lockfiles: nil,
			wantOK:    false,
		},
		{
			// Ambiguity keeps the meaning Manager gives it, and stops the walk:
			// a grandparent's lockfile resolves versions this package is not
			// installed under.
			name:      "an ambiguous root",
			lockfiles: map[string][]string{".": {"pnpm-lock.yaml"}, "packages": {"yarn.lock", "package-lock.json"}},
			wantOK:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := mkdir(t, t.TempDir(), "repo")
			dir := mkdir(t, root, "packages", "ui")
			for where, files := range tc.lockfiles {
				for _, f := range files {
					write(t, mkdir(t, root, filepath.FromSlash(where)), f)
				}
			}
			got, ok := WorkspaceRoot(dir, root)
			want := filepath.Join(root, filepath.FromSlash(tc.wantDir))
			if !tc.wantOK {
				want = ""
			}
			if ok != tc.wantOK || got != want {
				t.Fatalf("WorkspaceRoot = (%q, %v), want (%q, %v)", got, ok, want, tc.wantOK)
			}
		})
	}
}

// A lockfile above the scan root belongs to a tree this run was never asked
// about — a checkout inside someone's home directory must not install from it.
func TestTheWalkNeverClimbsAboveTheScanRoot(t *testing.T) {
	above := t.TempDir()
	write(t, above, "pnpm-lock.yaml")
	root := mkdir(t, above, "repo")
	dir := mkdir(t, root, "packages", "ui")

	if got, ok := WorkspaceRoot(dir, root); ok {
		t.Errorf("WorkspaceRoot = %q, want nothing above the scan root", got)
	}
	// The same tree with the scan root a level up does resolve it, so the bound
	// is what refused it and not the walk failing to reach.
	if got, ok := WorkspaceRoot(dir, above); !ok || got != above {
		t.Errorf("WorkspaceRoot = (%q, %v), want (%q, true)", got, ok, above)
	}
}

// A dir the bound does not contain bounds the walk at itself: an unrelated
// scan root must widen nothing.
func TestADirOutsideTheScanRootIsItsOwnBound(t *testing.T) {
	root := t.TempDir()
	dir := mkdir(t, root, "packages", "ui")
	write(t, root, "pnpm-lock.yaml")

	if got, ok := WorkspaceRoot(dir, t.TempDir()); ok {
		t.Errorf("WorkspaceRoot = %q, want the walk bounded at dir", got)
	}
	if got, ok := WorkspaceRoot(root, t.TempDir()); !ok || got != root {
		t.Errorf("WorkspaceRoot = (%q, %v), want dir's own lockfile at (%q, true)", got, ok, root)
	}
}

// The install runs where the lockfile is. A frozen install from the root
// installs the package too, and a package manager run in a directory holding
// no lockfile installs nothing.
func TestTheInstallRunsInTheResolvedRoot(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	dir := mkdir(t, root, "packages", "ui")
	write(t, root, "pnpm-lock.yaml")
	cwd := stubManager(t, "pnpm")

	if err := Install(context.Background(), dir, root, "", nil, io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := read(t, cwd); got != root {
		t.Errorf("pnpm ran in %q, want the workspace root %q", got, root)
	}
}

// An install that resolved no root runs nothing, rather than running a
// manager it had to guess in a directory that declares none.
func TestNoResolvedRootRunsNothing(t *testing.T) {
	dir := mkdir(t, t.TempDir(), "repo", "packages", "ui")
	cwd := stubManager(t, "pnpm")

	if err := Install(context.Background(), dir, filepath.Dir(filepath.Dir(dir)), "", nil, io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Error("an install ran with no lockfile to resolve it")
	}
}

// The override replaces detection entirely, including the walk: a repository
// that authored one said where it meant it to run by declaring the component
// there.
func TestTheOverrideRunsInTheComponentDirectory(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	dir := mkdir(t, root, "packages", "ui")
	write(t, root, "pnpm-lock.yaml")
	cwd := filepath.Join(t.TempDir(), "cwd")

	if err := Install(context.Background(), dir, root, "pwd > "+cwd, nil, io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := read(t, cwd); got != dir {
		t.Errorf("the override ran in %q, want the component directory %q", got, dir)
	}
}

// A frozen install run from inside a workspace package installs the whole
// workspace, so several components resolving one root would each rewrite the
// node_modules tree the others are reading from — a directory no component
// declares and the scheduler therefore locks nothing for.
func TestOneWorkspaceRootIsInstalledOnce(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	write(t, root, "pnpm-lock.yaml")
	runs := stubRecording(t, "pnpm", 0)

	var wg sync.WaitGroup
	for _, pkg := range []string{"ui", "core", "api", "web"} {
		dir := mkdir(t, root, "packages", pkg)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Install(context.Background(), dir, root, "", nil, io.Discard); err != nil {
				t.Errorf("Install: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := invocations(t, runs); got != 1 {
		t.Errorf("pnpm ran %d times, want one install for the shared root", got)
	}
}

// A root whose install failed has no tree to share, so the next component
// resolving it installs again rather than inheriting a failure it cannot
// explain.
func TestAFailedInstallIsNotTakenAsDone(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	write(t, root, "pnpm-lock.yaml")
	runs := stubRecording(t, "pnpm", 1)

	for _, pkg := range []string{"ui", "core"} {
		dir := mkdir(t, root, "packages", pkg)
		if err := Install(context.Background(), dir, root, "", nil, io.Discard); err == nil {
			t.Fatalf("%s: Install reported success for a failing manager", pkg)
		}
	}

	if got := invocations(t, runs); got != 2 {
		t.Errorf("pnpm ran %d times, want each caller to retry a failed install", got)
	}
}

// A second component naming a different environment for a root already
// installed is an error, not a silent share of whichever declaration got
// there first: the tree it imports from was written under one environment,
// and the other component asked for a different one.
func TestASecondEnvironmentForOneRootIsAnError(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	write(t, root, "pnpm-lock.yaml")
	stubRecording(t, "pnpm", 0)

	ui := mkdir(t, root, "packages", "ui")
	if err := Install(context.Background(), ui, root, "", []string{"TOKEN=a", "OTHER=x"}, io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
	}

	api := mkdir(t, root, "packages", "api")
	if err := Install(context.Background(), api, root, "", []string{"TOKEN=b", "OTHER=x"}, io.Discard); err == nil {
		t.Error("Install reported success for a root already installed under a different environment")
	}

	web := mkdir(t, root, "packages", "web")
	if err := Install(context.Background(), web, root, "", []string{"TOKEN=a", "OTHER=x", "THIRD=y"}, io.Discard); err == nil {
		t.Error("Install reported success for an environment differing by an added variable")
	}
}

// envEqual sorts both sides before comparing, so order within either
// declaration must not matter. Each side is unsorted here, and in a
// different order from the other — the two-element case a reordered
// component naturally produces can leave one side already coincidentally in
// the other's sorted order, silently masking a comparison that never sorted
// it at all.
func TestEnvEqualSortsBothSidesIndependently(t *testing.T) {
	a := []string{"X=1", "A=2", "M=3"}
	b := []string{"M=3", "X=1", "A=2"}
	if !envEqual(a, b) {
		t.Errorf("envEqual(%v, %v) = false, want true — same set, different order on each side", a, b)
	}
	if !envEqual(b, a) {
		t.Errorf("envEqual(%v, %v) = false, want true — order of the arguments must not matter either", b, a)
	}
	if envEqual(a, []string{"X=1", "A=2", "N=3"}) {
		t.Error("envEqual reported two different sets as equal")
	}
}

// stubRecording puts a program of the given name ahead of any real one on
// PATH, writing one file per invocation into the returned directory and
// exiting with code. It sleeps long enough that a concurrent caller arrives
// while it is running, so a test counting invocations counts a race that
// actually happened.
func stubRecording(t *testing.T, name string, code int) string {
	t.Helper()
	bin := t.TempDir()
	runs := mkdir(t, t.TempDir(), "runs")
	script := "#!/bin/sh\nmktemp " + filepath.Join(runs, "run.XXXXXX") + " >/dev/null\nsleep 0.2\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { // #nosec G306 -- a stub that has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return runs
}

// invocations counts the files stubRecording's program left behind.
func invocations(t *testing.T, runs string) int {
	t.Helper()
	entries, err := os.ReadDir(runs)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// stubManager puts a program of the given name ahead of any real one on PATH,
// recording the directory it was run in at the returned path. Nothing here
// runs a real package manager: an install that reaches the network tests the
// machine it runs on.
func stubManager(t *testing.T, name string) string {
	t.Helper()
	bin := t.TempDir()
	cwd := filepath.Join(bin, "cwd")
	script := "#!/bin/sh\npwd > " + cwd + "\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { // #nosec G306 -- a stub that has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cwd
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// The installed tree is what a coverage producer is read from, so this answers
// with what is on disk rather than with what a lockfile intended.
func TestPackageVersionReadsTheInstalledTree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "node_modules", "vitest")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":"4.1.11"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := PackageVersion(root, "vitest"); !ok || got != "4.1.11" {
		t.Errorf("PackageVersion = (%q, %v), want 4.1.11", got, ok)
	}
}

// A scoped package is a nested directory, which is the form every coverage
// provider is published under.
func TestPackageVersionReadsAScopedPackage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "node_modules", "@vitest", "coverage-v8")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":"4.1.11"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := PackageVersion(root, "@vitest/coverage-v8"); !ok || got != "4.1.11" {
		t.Errorf("PackageVersion = (%q, %v), want 4.1.11", got, ok)
	}
}

// Absent, unreadable and version-less all answer false. A caller that cannot
// identify a package must say so rather than record a producer it guessed.
func TestPackageVersionReportsWhatItCannotRead(t *testing.T) {
	root := t.TempDir()
	if _, ok := PackageVersion(root, "vitest"); ok {
		t.Error("PackageVersion found a package in an empty tree")
	}
	dir := filepath.Join(root, "node_modules", "broken")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"broken"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := PackageVersion(root, "broken"); ok {
		t.Error("PackageVersion answered for a manifest carrying no version")
	}
}
