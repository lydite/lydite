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

// packageManager adds an exact version to the manager the lockfile identifies
// and never overrides it: an absent field leaves lockfile detection as the
// answer, ambiguous lockfiles stay refused, and a field that disagrees with
// its lockfile or cannot be read as a pin is an error rather than a guess.
func TestPackageManager(t *testing.T) {
	for _, tc := range []struct {
		name      string
		manifest  string // "" writes no package.json
		lockfiles []string
		want      Declared
		wantOK    bool
		wantErr   bool
	}{
		{
			name:      "a pinned version",
			manifest:  `{"packageManager":"pnpm@8.15.4"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
			want:      Declared{Name: "pnpm", Version: "8.15.4"},
			wantOK:    true,
		},
		{
			name:      "an integrity hash is carried apart from the version",
			manifest:  `{"packageManager":"yarn@4.1.0+sha512.abc123"}`,
			lockfiles: []string{"yarn.lock"},
			want:      Declared{Name: "yarn", Version: "4.1.0", Hash: "sha512.abc123"},
			wantOK:    true,
		},
		{
			name:      "a prerelease",
			manifest:  `{"packageManager":"npm@11.0.0-pre.1"}`,
			lockfiles: []string{"package-lock.json"},
			want:      Declared{Name: "npm", Version: "11.0.0-pre.1"},
			wantOK:    true,
		},
		{
			name:      "no field",
			manifest:  `{"name":"app"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
		},
		{
			name:      "no package.json",
			lockfiles: []string{"pnpm-lock.yaml"},
		},
		{
			// The field only adds a version to a manager the lockfile names;
			// with no lockfile there is no install for it to pin.
			name:     "no lockfile",
			manifest: `{"packageManager":"pnpm@8.15.4"}`,
		},
		{
			// Ambiguity keeps the meaning Manager gives it.
			name:      "ambiguous lockfiles",
			manifest:  `{"packageManager":"pnpm@8.15.4"}`,
			lockfiles: []string{"pnpm-lock.yaml", "yarn.lock"},
		},
		{
			name:      "a field disagreeing with its lockfile",
			manifest:  `{"packageManager":"yarn@4.1.0"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
			wantErr:   true,
		},
		{
			name:      "an unrecognised manager",
			manifest:  `{"packageManager":"bun@1.1.0"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
			wantErr:   true,
		},
		{
			name:      "no version",
			manifest:  `{"packageManager":"pnpm"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
			wantErr:   true,
		},
		{
			name:      "a range is not a pin",
			manifest:  `{"packageManager":"pnpm@^8.15.4"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
			wantErr:   true,
		},
		{
			name:      "a partial version is not a pin",
			manifest:  `{"packageManager":"pnpm@8"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
			wantErr:   true,
		},
		{
			name:      "no name",
			manifest:  `{"packageManager":"@8.15.4"}`,
			lockfiles: []string{"pnpm-lock.yaml"},
			wantErr:   true,
		},
		{
			name:      "a manifest that is not JSON",
			manifest:  `{"packageManager":`,
			lockfiles: []string{"pnpm-lock.yaml"},
			wantErr:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, lf := range tc.lockfiles {
				write(t, dir, lf)
			}
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(tc.manifest), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, err := PackageManager(dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("PackageManager error = %v, want error %v", err, tc.wantErr)
			}
			want := tc.want
			if tc.wantOK {
				want.Source = filepath.Join(dir, "package.json")
			}
			if ok != tc.wantOK || got != want {
				t.Fatalf("PackageManager = (%+v, %v), want (%+v, %v)", got, ok, want, tc.wantOK)
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

// yarn is provisioned by internal/toolchain the same way pnpm is, upstream of
// Commands being called, so there is nothing left for a corepack step to do.
func TestYarnIsASingleCommand(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "yarn.lock")
	cmds := Commands(dir, "")
	if len(cmds) != 1 || cmds[0].Argv[0] != "yarn" {
		t.Fatalf("Commands = %v, want a single yarn command", cmds)
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

// An override replaces what runs, not where: at a root the walk resolves it
// runs there, exactly as the detected install it replaces would have.
func TestTheOverrideRunsInTheResolvedRoot(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	dir := mkdir(t, root, "packages", "ui")
	write(t, root, "pnpm-lock.yaml")
	cwd := filepath.Join(t.TempDir(), "cwd")

	if err := Install(context.Background(), dir, root, "pwd > "+cwd, nil, io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := read(t, cwd); got != root {
		t.Errorf("the override ran in %q, want the workspace root %q", got, root)
	}
}

// Where no single root resolves there is nothing to coalesce against, and the
// ambiguous root is one of the cases the override exists for, so it runs in
// the component's own directory rather than not at all.
func TestAnOverrideWithNoResolvedRootRunsInTheComponentDirectory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lockfiles []string
	}{
		{"an ambiguous root", []string{"pnpm-lock.yaml", "yarn.lock"}},
		{"no lockfile", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := mkdir(t, t.TempDir(), "repo")
			dir := mkdir(t, root, "packages", "ui")
			for _, lf := range tc.lockfiles {
				write(t, root, lf)
			}
			cwd := filepath.Join(t.TempDir(), "cwd")

			if err := Install(context.Background(), dir, root, "pwd > "+cwd, nil, io.Discard); err != nil {
				t.Fatalf("Install: %v", err)
			}
			if got := read(t, cwd); got != dir {
				t.Errorf("the override ran in %q, want the component directory %q", got, dir)
			}
		})
	}
}

// An override at a resolved root is that root's install, so components
// sharing the root share it too. Run once per component instead, each copy
// rewrites the node_modules tree, lockfile and store the others are reading.
func TestAnOverrideAtOneRootIsInstalledOnce(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	write(t, root, "pnpm-lock.yaml")
	runs := mkdir(t, t.TempDir(), "runs")
	// Sleeps long enough that a concurrent caller arrives while it runs, so the
	// count below reflects a race that actually happened.
	override := "mktemp " + filepath.Join(runs, "run.XXXXXX") + " >/dev/null && sleep 0.2"

	var wg sync.WaitGroup
	for _, pkg := range []string{"ui", "core", "api", "web"} {
		dir := mkdir(t, root, "packages", pkg)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Install(context.Background(), dir, root, override, nil, io.Discard); err != nil {
				t.Errorf("Install: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := invocations(t, runs); got != 1 {
		t.Errorf("the override ran %d times, want one install for the shared root", got)
	}
}

// The override's single install at a root runs under one environment, so a
// second component naming a different one is refused exactly as it is for a
// detected install.
func TestASecondEnvironmentForOneOverriddenRootIsAnError(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	write(t, root, "pnpm-lock.yaml")
	override := "true"

	ui := mkdir(t, root, "packages", "ui")
	if err := Install(context.Background(), ui, root, override, []string{"TOKEN=a"}, io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
	}

	api := mkdir(t, root, "packages", "api")
	if err := Install(context.Background(), api, root, override, []string{"TOKEN=b"}, io.Discard); err == nil {
		t.Error("Install reported success for an overridden root already installed under a different environment")
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

// A manager task 2 was supposed to have provisioned but did not reach this
// component's environment fails with a name and a reason, not exec's bare
// "executable file not found in $PATH".
func TestAnAbsentManagerFailsWithAName(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	write(t, root, "pnpm-lock.yaml")
	// A PATH composed for the child that carries no pnpm at all, and this
	// process's own PATH left untouched — the failure must come from the
	// composed env, not from whatever this test binary happens to have.
	env := []string{"PATH=" + t.TempDir()}

	err := Install(context.Background(), root, root, "", env, io.Discard)
	if err == nil {
		t.Fatal("Install reported success with pnpm absent from the composed PATH")
	}
	if !strings.Contains(err.Error(), "pnpm: not on PATH") {
		t.Errorf("Install error = %q, want it to name pnpm and say it is not on PATH", err.Error())
	}
}

// The manager PATH check only applies to lydite's own detected-manager
// commands. An override is authored by whoever configured the repository,
// and lydite has no business validating a shell command it did not build.
func TestAnOverrideIsNotCheckedAgainstPATH(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	cwd := filepath.Join(t.TempDir(), "cwd")
	env := []string{"PATH=" + t.TempDir()}

	if err := Install(context.Background(), root, root, "pwd > "+cwd, env, io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
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
