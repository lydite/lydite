package reviewstages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/reviewdecision"
	"lydite/lydite/internal/toolchain"
)

// reviewScan is what reviewScanReader answers for one report directory.
type reviewScan struct {
	rows []reviewdecision.GateRow
	err  error
}

// reviewScanReader is a reviewdecision.ScanReader answering from a fixed map
// of report directories, recording which directories it was asked for, in
// order. A directory absent from the map holds no scan document.
type reviewScanReader struct {
	scans map[string]reviewScan
	asked []string
}

func (r *reviewScanReader) ReadScan(reports string) ([]reviewdecision.GateRow, error) {
	r.asked = append(r.asked, reports)
	scan, ok := r.scans[reports]
	if !ok {
		return nil, fmt.Errorf("%s: %w", reports, reviewdecision.ErrNoScanDocument)
	}
	return scan.rows, scan.err
}

// refusingToolchains is a reviewdecision.Toolchains for a run that must never
// provision one: a tree where nothing opted in, or a comparison read from a
// document rather than made.
type refusingToolchains struct{ t *testing.T }

func (r refusingToolchains) Ensure(context.Context, string, config.Config, []component.Component) (toolchain.Envs, error) {
	r.t.Fatal("Toolchains.Ensure: unexpected call")
	return nil, nil
}

func (r refusingToolchains) CheckEnv(*toolchain.Env, component.Component) []string {
	r.t.Fatal("Toolchains.CheckEnv: unexpected call")
	return nil
}

// cancellingToolchains provisions nothing, and records whether a component's
// own code was about to be built under it: CheckEnv is asked for exactly when
// a comparison that executes the tree under review is about to run. It
// cancels the run's context there, so the comparison it heralds stops before
// it installs or builds anything.
type cancellingToolchains struct {
	cancel      context.CancelFunc
	checkEnvFor []string
}

func (c *cancellingToolchains) Ensure(context.Context, string, config.Config, []component.Component) (toolchain.Envs, error) {
	return nil, nil
}

func (c *cancellingToolchains) CheckEnv(_ *toolchain.Env, comp component.Component) []string {
	c.checkEnvFor = append(c.checkEnvFor, comp.Name)
	c.cancel()
	return nil
}

// git runs git in dir and fails the test when it does not succeed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := executil.RunQuiet(context.Background(), dir, "git", args...)
	if !r.Ok() {
		t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
	}
	return strings.TrimSpace(r.Output)
}

// writeFiles writes each file under dir, creating its directory.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// checkout is a repository committing each of commits in turn, the first as
// the base, and returns the directory and the SHA of every commit in order.
func checkout(t *testing.T, commits ...map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--quiet", "-b", "main")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	shas := make([]string, 0, len(commits))
	for i, files := range commits {
		writeFiles(t, dir, files)
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "--quiet", "--allow-empty", "-m", fmt.Sprintf("commit %d", i))
		shas = append(shas, git(t, dir, "rev-parse", "HEAD"))
	}
	return dir, shas
}

// eventFile writes payload as an event file and returns its path.
func eventFile(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A Rust crate opted into a comparison, and the config that keeps
// provisioning out of the run.
const (
	crateCargoToml = "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"
	crateAPI       = "pub fn do_thing(n: i32) -> i32 {\n    n\n}\n"
	crateOptIn     = "components:\n  - name: probe\n    dir: probe\n    runner: cargo-nextest\n    api_surface: {}\n"
	noProvisioning = "toolchain:\n  enabled: false\n"
)

func crateBase() map[string]string {
	return map[string]string{
		"README.md":        "hello",
		component.FileName: crateOptIn,
		config.FileName:    noProvisioning,
		"probe/Cargo.toml": crateCargoToml,
		"probe/src/lib.rs": crateAPI,
	}
}
