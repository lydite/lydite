package toolchain

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/detect"
)

// The whole point of the "prefer ambient" rule: a runner that already has a
// good-enough toolchain must produce no download and no install. This pins
// the decision itself, independent of any provisioner.
func TestSatisfied(t *testing.T) {
	pinned := Requirement{Ecosystem: detect.Go, Version: "v1.26.4", Raw: "1.26.4"}
	unpinned := Requirement{Ecosystem: detect.Rust, Raw: "stable"}

	for _, tc := range []struct {
		name    string
		req     Requirement
		ambient string
		present bool
		want    bool
	}{
		{"exact match is satisfied", pinned, "v1.26.4", true, true},
		{"newer ambient is satisfied", pinned, "v1.26.5", true, true},
		{"older ambient is not", pinned, "v1.25.0", true, false},
		{"absent is not, however new the pin", pinned, "", false, false},
		// An unpinned requirement names no floor, so anything present clears it.
		{"unpinned is satisfied by anything present", unpinned, "v1.80.0", true, true},
		{"unpinned still needs something present", unpinned, "", false, false},
		// A toolchain that will not identify itself cannot be shown to
		// satisfy a pin, so it is treated as too old.
		{"unidentifiable ambient fails a pin", pinned, "", true, false},
		{"unidentifiable ambient clears no pin", unpinned, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := satisfied(tc.req, tc.ambient, tc.present); got != tc.want {
				t.Fatalf("satisfied(%+v, %q, %v) = %v, want %v", tc.req, tc.ambient, tc.present, got, tc.want)
			}
		})
	}
}

// With the ambient toolchain already good enough, Ensure must install
// nothing: no PATH entry, and — critically — no network access, which is what
// makes this test safe to run offline at all.
func TestEnsureInstallsNothingWhenAmbientSatisfies(t *testing.T) {
	dir := t.TempDir()
	// A go.mod pinning something ancient, so whatever Go is running these
	// tests necessarily satisfies it.
	write(t, dir, "go.mod", "module x\n\ngo 1.16\n")

	var log bytes.Buffer
	env, err := Ensure(context.Background(), dir, []detect.Ecosystem{detect.Go}, Overrides{}, &log)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(env.PathDirs) != 0 {
		t.Fatalf("a satisfied toolchain must add nothing to PATH, got %+v", env.PathDirs)
	}
	if !strings.Contains(log.String(), "using ambient go") {
		t.Errorf("Ensure should say it reused the ambient toolchain; log was %q", log.String())
	}
}

// Satisfied is not the same as nothing to do, and this is the case that bit
// for real. GOTOOLCHAIN=auto does not only upgrade: `go install <tool>@<ver>`
// outside a module consults that TOOL's go.mod and switches to the minimum it
// declares, so golang.org/x/vuln (go >= 1.25.0) builds govulncheck with go1.25
// on a runner whose Go is 1.26 — and a govulncheck built by an older Go
// rejects newer source outright. Verified against the real thing: an
// unpinned `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` in this
// repo downgrades to go1.25.13 and fails to load the packages, while the same
// command with GOTOOLCHAIN set completes cleanly.
func TestEnsurePinsGoToolchainEvenWhenAmbientSatisfies(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.16\n")

	env, err := Ensure(context.Background(), dir, []detect.Ecosystem{detect.Go}, Overrides{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// "local", not the declared version: the declaration is a minimum, so
	// pinning it would downgrade a newer ambient toolchain — precisely
	// backwards when the newer patch is the one carrying a security fix.
	want := "GOTOOLCHAIN=local"
	if len(env.Vars) != 1 || env.Vars[0] != want {
		t.Fatalf("Vars = %+v, want exactly [%s]", env.Vars, want)
	}
}

// With nothing declared, nothing was verified, so there is no ground for
// overriding whatever the environment already chose.
func TestEnsureDoesNotPinGoWhenNothingIsDeclared(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n")

	env, err := Ensure(context.Background(), dir, []detect.Ecosystem{detect.Go}, Overrides{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(env.Vars) != 0 {
		t.Fatalf("Vars = %+v, want none when the repo declares no Go version", env.Vars)
	}
}

// Disabling provisioning must still diagnose. An air-gapped runner gets to
// know its toolchain is too old without lydite trying to fix it — silence
// would be the failure mode this package exists to remove.
func TestEnsureDisabledStillReportsShortfall(t *testing.T) {
	dir := t.TempDir()
	// Far in the future, so no real toolchain can satisfy it.
	write(t, dir, "go.mod", "module x\n\ngo 99.0.0\n")

	var log bytes.Buffer
	env, err := Ensure(context.Background(), dir, []detect.Ecosystem{detect.Go}, Overrides{Disabled: true}, &log)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(env.PathDirs) != 0 || len(env.Vars) != 0 {
		t.Fatalf("provisioning was disabled but the env changed: %+v", env)
	}
	out := log.String()
	if !strings.Contains(out, "toolchain.enabled is false") {
		t.Errorf("log should name the disabling setting, got %q", out)
	}
	if !strings.Contains(out, "99.0.0") {
		t.Errorf("log should name the version that went unsatisfied, got %q", out)
	}
}

// A provisioning failure is a warning, not a scan failure: this step is
// preparation, and falling through to "whatever is on PATH" is exactly
// today's behavior. Rust with no rustup is the cheapest way to reach that
// path without touching the network.
func TestEnsureProvisioningFailureWarnsAndContinues(t *testing.T) {
	if _, err := os.Stat("/nonexistent"); err == nil {
		t.Skip("unexpected filesystem layout")
	}
	dir := t.TempDir()
	write(t, dir, "Cargo.toml", "[package]\nname = \"x\"\n")
	write(t, dir, "rust-toolchain.toml", "[toolchain]\nchannel = \"99.0.0\"\n")

	// Empty PATH: neither cargo nor rustup is findable, so provisionRust
	// fails at once with no network involved.
	t.Setenv("PATH", "")

	var log bytes.Buffer
	env, err := Ensure(context.Background(), dir, []detect.Ecosystem{detect.Rust}, Overrides{}, &log)
	if err != nil {
		t.Fatalf("Ensure returned an error; a provisioning failure must be a warning: %v", err)
	}
	if len(env.PathDirs) != 0 {
		t.Fatalf("a failed provision must contribute nothing to the env, got %+v", env)
	}
	if !strings.Contains(log.String(), "warning:") {
		t.Errorf("a failed provision must be named on the log, got %q", log.String())
	}
}

func TestEnvActivatePrependsPathAndSetsVars(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GOTOOLCHAIN", "auto")

	env := &Env{
		PathDirs: []string{"/opt/go/bin", "/opt/node/bin"},
		Vars:     []string{"GOTOOLCHAIN=go1.26.5"},
	}
	if err := env.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	sep := string(os.PathListSeparator)
	want := "/opt/go/bin" + sep + "/opt/node/bin" + sep + "/usr/bin"
	if got := os.Getenv("PATH"); got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
	if got := os.Getenv("GOTOOLCHAIN"); got != "go1.26.5" {
		t.Errorf("GOTOOLCHAIN = %q, want go1.26.5", got)
	}
}

// A provisioned toolchain exists precisely because the ambient one was
// missing or too old, so it has to come first on PATH — appending would
// install it and then never use it.
func TestEnvActivatePrependsRatherThanAppends(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	env := &Env{PathDirs: []string{"/opt/go/bin"}}
	if err := env.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !strings.HasPrefix(os.Getenv("PATH"), "/opt/go/bin") {
		t.Fatalf("PATH = %q, want the provisioned dir first", os.Getenv("PATH"))
	}
}

func TestEnvActivateNilIsSafe(t *testing.T) {
	var env *Env
	if err := env.Activate(); err != nil {
		t.Fatalf("Activate on a nil Env: %v", err)
	}
}

// installOnce is what stops an interrupted download leaving a half-populated
// directory that the next run treats as a finished install.
func TestInstallOnceIsAtomicAndSkipsExisting(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "go-1.26.5")

	// A failing install must leave nothing behind.
	err := installOnce(dir, func(staging string) error {
		if writeErr := os.WriteFile(filepath.Join(staging, "partial"), []byte("x"), 0o600); writeErr != nil {
			return writeErr
		}
		return os.ErrDeadlineExceeded
	})
	if err == nil {
		t.Fatal("installOnce should surface the install error")
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Fatal("a failed install left the destination directory in place")
	}

	// A successful one lands the content.
	if err := installOnce(dir, func(staging string) error {
		return os.WriteFile(filepath.Join(staging, "ok"), []byte("y"), 0o600)
	}); err != nil {
		t.Fatalf("installOnce: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok")); err != nil {
		t.Fatalf("successful install did not land: %v", err)
	}

	// A second call must not re-run the installer.
	called := false
	if err := installOnce(dir, func(string) error { called = true; return nil }); err != nil {
		t.Fatalf("installOnce on an existing dir: %v", err)
	}
	if called {
		t.Error("installOnce re-ran the installer for an already-present toolchain")
	}
}
