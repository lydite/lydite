package toolchain

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/runner"
)

// The whole point of the "prefer ambient" rule: a runner that already has a
// good-enough toolchain must produce no download and no install. This pins
// the decision itself, independent of any provisioner.
func TestSatisfied(t *testing.T) {
	pinned := Requirement{Lang: runner.Go, Version: "v1.26.4", Raw: "1.26.4"}
	unpinned := Requirement{Lang: runner.Rust, Raw: "stable"}

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

// ensureOne resolves one component rooted at the scan root and returns its
// environment, which is nil when there is nothing to apply.
func ensureOne(t *testing.T, dir string, l runner.Lang, ov Overrides, log io.Writer) *Env {
	t.Helper()
	envs, err := Ensure(context.Background(), dir, []Unit{{Name: "c", Lang: l, Dir: "."}}, ov, log)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return envs.For("c")
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
	env := ensureOne(t, dir, runner.Go, Overrides{}, &log)
	for _, kv := range env.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			t.Fatalf("a satisfied toolchain must add nothing to PATH, got %q", kv)
		}
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

	env := ensureOne(t, dir, runner.Go, Overrides{}, &bytes.Buffer{})
	// "local", not the declared version: the declaration is a minimum, so
	// pinning it would downgrade a newer ambient toolchain — precisely
	// backwards when the newer patch is the one carrying a security fix.
	want := "GOTOOLCHAIN=local"
	if got := env.Environ(); len(got) != 1 || got[0] != want {
		t.Fatalf("Environ = %+v, want exactly [%s]", got, want)
	}
}

// With nothing declared, nothing was verified, so there is no ground for
// overriding whatever the environment already chose.
func TestEnsureDoesNotPinGoWhenNothingIsDeclared(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n")

	env := ensureOne(t, dir, runner.Go, Overrides{}, &bytes.Buffer{})
	if got := env.Environ(); len(got) != 0 {
		t.Fatalf("Environ = %+v, want none when the repo declares no Go version", got)
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
	env := ensureOne(t, dir, runner.Go, Overrides{Disabled: true}, &log)
	if got := env.Environ(); len(got) != 0 {
		t.Fatalf("provisioning was disabled but the env changed: %+v", got)
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
	// ensureOne fails the test if Ensure returns an error at all: a
	// provisioning failure must be a warning, never a verdict.
	env := ensureOne(t, dir, runner.Rust, Overrides{}, &log)
	if len(env.Environ()) != 0 {
		t.Fatalf("a failed provision must contribute nothing to the env, got %+v", env)
	}
	if !strings.Contains(log.String(), "warning:") {
		t.Errorf("a failed provision must be named on the log, got %q", log.String())
	}
}

func TestComposeBuildsOnePathEntryAndKeepsVars(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	env := &Env{
		PathDirs: []string{"/opt/go/bin", "/opt/node/bin"},
		Vars:     []string{"GOTOOLCHAIN=go1.26.5"},
	}
	got := env.Environ()
	sep := string(os.PathListSeparator)
	want := []string{
		"GOTOOLCHAIN=go1.26.5",
		"PATH=/opt/go/bin" + sep + "/opt/node/bin" + sep + "/usr/bin",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Environ() = %q, want %q", got, want)
	}
}

// The defect deleting the process-wide activation would otherwise create: a
// child's environment is a flat list where the last occurrence of a key wins,
// so two callers each prepending their own directories produce two PATH
// entries and one is discarded with nothing to show for it. Compose takes
// every caller's directories and emits one.
func TestComposeEmitsExactlyOnePathEntry(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	got := Compose([]string{"/pinned/bin", "/opt/go/bin"}, nil, []string{"A=1"}, []string{"B=2"})
	var paths int
	for _, kv := range got {
		if strings.HasPrefix(kv, "PATH=") {
			paths++
		}
	}
	if paths != 1 {
		t.Fatalf("Environ = %q, want exactly one PATH entry", got)
	}
	sep := string(os.PathListSeparator)
	want := "PATH=/pinned/bin" + sep + "/opt/go/bin" + sep + "/usr/bin"
	if got[len(got)-1] != want {
		t.Fatalf("PATH = %q, want %q — the nearest caller's directory first", got[len(got)-1], want)
	}
}

// Nothing to prepend must leave PATH alone entirely rather than restating it,
// so a component with no provisioned toolchain runs with the environment it
// would have had.
func TestComposeWithNoDirsSetsNoPath(t *testing.T) {
	got := Compose(nil, nil, []string{"A=1"})
	if !slices.Equal(got, []string{"A=1"}) {
		t.Fatalf("Compose = %q, want just the variables", got)
	}
}

func TestNilEnvComposesNothing(t *testing.T) {
	var env *Env
	if got := env.Environ(); got != nil {
		t.Fatalf("Environ on a nil Env = %q, want nil", got)
	}
	if got := Envs(nil).For("c"); got != nil {
		t.Fatalf("For on a nil Envs = %+v, want nil", got)
	}
}

// Two components asking for the same toolchain are probed and reported once:
// the second one reuses the first's answer rather than repeating both the
// work and the diagnostic.
func TestEnsureSharesOneResultAcrossComponentsWantingTheSameThing(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a"), "go.mod", "module a\n\ngo 1.16\n")
	write(t, filepath.Join(dir, "b"), "go.mod", "module b\n\ngo 1.16\n")

	var log bytes.Buffer
	envs, err := Ensure(context.Background(), dir, []Unit{
		{Name: "a", Lang: runner.Go, Dir: "a"},
		{Name: "b", Lang: runner.Go, Dir: "b"},
	}, Overrides{}, &log)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if envs.For("a") != envs.For("b") {
		t.Errorf("two components wanting one toolchain got two results")
	}
	if n := strings.Count(log.String(), "using ambient go"); n != 1 {
		t.Errorf("the ambient toolchain was reported %d times, want once", n)
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

// "Nothing is declared" is now a statement about one component's directory
// rather than about the repository, and for Go it is also the reason
// GOTOOLCHAIN goes unpinned — the quietest thing this package does. The
// diagnostic names the component so a reader can go and look at the right
// directory.
func TestAnUnpinnedRequirementNamesTheComponent(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "svc"), "go.mod", "module x\n")

	var log bytes.Buffer
	if _, err := Ensure(context.Background(), dir,
		[]Unit{{Name: "svc", Lang: runner.Go, Dir: "svc"}}, Overrides{}, &log); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !strings.Contains(log.String(), "component svc declares no version") {
		t.Errorf("log = %q, want it to name the component that declared nothing", log.String())
	}
}

// An empty inherited PATH must not leave a trailing separator. An empty PATH
// element means the current directory to a shell and to an exec lookup, and a
// component's commands run with their working directory set inside the
// repository being scanned — so the trailing separator would put that
// repository on the child's PATH.
func TestComposeDoesNotPutTheWorkingDirectoryOnPath(t *testing.T) {
	t.Setenv("PATH", "")

	got := Compose([]string{"/opt/go/bin"}, nil, nil)
	if len(got) != 1 {
		t.Fatalf("Compose = %q, want one PATH entry", got)
	}
	if got[0] != "PATH=/opt/go/bin" {
		t.Fatalf("PATH = %q, want no trailing separator", got[0])
	}
}

// A directory the scanned repository asked for goes behind the inherited PATH,
// so it can add a binary that exists nowhere else and cannot replace one
// lydite resolved. Ahead of it, a repository shipping its own `go` would
// decide which toolchain lydite runs.
func TestComposeKeepsATrailingDirectoryBehindTheInheritedPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	got := Compose([]string{"/resolved/bin"}, []string{"/declared/bin"}, nil)
	sep := string(os.PathListSeparator)
	want := "PATH=/resolved/bin" + sep + "/usr/bin" + sep + "/declared/bin"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Compose = %q, want %q", got, want)
	}
}

// A tool cache keyed on this must survive a toolchain upgrade. An ambient Go
// that already satisfies the declaration contributes no directory and only
// GOTOOLCHAIN=local, so without the resolved version every such component on
// every machine hashes alike — and a CI image bumped 1.25 to 1.26 keeps
// reusing a tool built by 1.25, which rejects 1.26 source outright.
func TestKeyDistinguishesAmbientToolchainVersions(t *testing.T) {
	older := (&Env{Vars: []string{"GOTOOLCHAIN=local"}, Resolved: "1.25.4"}).Key()
	newer := (&Env{Vars: []string{"GOTOOLCHAIN=local"}, Resolved: "1.26.6"}).Key()
	if older == newer {
		t.Fatalf("two ambient toolchains share the key %q, so a tool built by the older one is reused", older)
	}
}
