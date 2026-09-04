package nodedeps

import (
	"os"
	"path/filepath"
	"strings"
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
