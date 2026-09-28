package run

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// fakePnpm puts a pnpm on PATH that leaves a marker in the directory it
// installs, so a test can tell whether an install ran and where.
func fakePnpm(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	// #nosec G306 -- a stand-in executable, in a temp dir
	if err := os.WriteFile(filepath.Join(bin, "pnpm"), []byte("#!/bin/sh\ntouch \"$PWD/installed\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// pnpmWorkspace writes a workspace at dir pinning pnpm at version, holding
// one package, and returns that package's component.
func pnpmWorkspace(t *testing.T, root, dir, version string, r runner.Name) component.Component {
	t.Helper()
	write(t, root, dir+"/package.json", `{"name":"`+dir+`","packageManager":"pnpm@`+version+`"}`)
	write(t, root, dir+"/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	write(t, root, dir+"/packages/ui/package.json", `{"name":"ui"}`)
	c := component.Component{Name: dir, Dir: dir + "/packages/ui", Runner: r}
	if r == "" {
		c.Command = []string{"sh", "-c", "exit 0"}
	}
	return c
}

func prepareIn(t *testing.T, root string, c component.Component, cfg config.Config) (ui.Row, bool) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(c.Dir))
	return Prepare(context.Background(), runner.Invocation{}, dir, root, TestLabel(c.Name), c, cfg, nil, Output{W: io.Discard})
}

func installedAt(root, dir string) bool {
	_, err := os.Stat(filepath.Join(root, dir, "installed"))
	return err == nil
}

// A pnpm pin below 12 fails its own component's install, naming the file and
// the pin, and never runs the pnpm on PATH; a component under a supported pin
// in the same run installs as usual. Both a runner's install and a raw
// command's go through the refusal.
func TestAPnpmPinBelowTwelveFailsOnlyItsOwnInstall(t *testing.T) {
	for name, r := range map[string]runner.Name{"a runner": runner.Vitest, "a command": ""} {
		t.Run(name, func(t *testing.T) {
			fakePnpm(t)
			root := t.TempDir()
			old := pnpmWorkspace(t, root, "old", "10.18.3+sha512."+strings.Repeat("a", 128), r)
			current := pnpmWorkspace(t, root, "new", "12.4.1", r)
			cfg := config.Default()

			row, ok := prepareIn(t, root, old, cfg)
			if ok || row.Status != ui.StatusFail || row.Value != "not prepared" {
				t.Fatalf("row = %+v, ok = %v, want a failed preparation", row, ok)
			}
			want := "lydite requires pnpm ≥ 12; old/package.json pins pnpm@10.18.3"
			if detail := strings.Join(row.Detail, " "); !strings.Contains(detail, want) || !strings.Contains(detail, "toolchain.enabled: false") {
				t.Errorf("detail = %q, want %q and the escape hatch", detail, want)
			}
			if installedAt(root, "old") {
				t.Error("a refused pin must not run the pnpm on PATH")
			}

			if row, ok := prepareIn(t, root, current, cfg); !ok {
				t.Fatalf("a supported pin in the same run failed: %+v", row)
			}
			if !installedAt(root, "new") {
				t.Error("the supported pin's workspace was not installed")
			}
		})
	}
}

// toolchain.enabled: false leaves the install to whatever is on PATH, so a
// pnpm pin below 12 installs with the ambient pnpm.
func TestProvisioningDisabledLetsAPnpmPinBelowTwelveThrough(t *testing.T) {
	fakePnpm(t)
	root := t.TempDir()
	c := pnpmWorkspace(t, root, "old", "10.18.3", runner.Vitest)
	cfg := config.Default()
	cfg.Toolchain.Enabled = false

	if row, ok := prepareIn(t, root, c, cfg); !ok {
		t.Fatalf("row = %+v, want the install to run", row)
	}
	if !installedAt(root, "old") {
		t.Error("the ambient pnpm did not install the workspace")
	}
}
