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

// Two components asking for the same toolchain are probed once and told about
// separately. The work is shared — the second reuses the first's result rather
// than probing the machine again — but the line is per component, because what
// it says is about one directory: "component api declares no version" printed
// once for a repository leaves every other component unpinned in silence.
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
	if n := strings.Count(log.String(), "using ambient go"); n != 2 {
		t.Errorf("the ambient toolchain was reported %d times, want once per component", n)
	}
	// The pin is about the machine rather than any one component, so it is
	// said once however many components share the toolchain.
	if n := strings.Count(log.String(), "pinned GOTOOLCHAIN"); n != 1 {
		t.Errorf("the toolchain pin was reported %d times, want once for the run", n)
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
//
// Driven through Ensure rather than a hand-built Env: the branch this exists
// for is the ambient one, and a test that constructs the value itself passes
// whether or not production ever sets it. It did not, and this is what caught
// it the second time.
func TestKeyDistinguishesAmbientToolchainVersions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.16\n")

	env := ensureOne(t, dir, runner.Go, Overrides{}, &bytes.Buffer{})
	if env == nil {
		t.Fatal("an ambient Go that satisfies the declaration still has GOTOOLCHAIN to pin")
		return
	}
	if env.Resolved == "" {
		t.Fatal("the resolved toolchain is unrecorded, so every ambient Go hashes to one key")
	}
	// The same environment with a different toolchain under it must key
	// differently, which is the whole point of recording it.
	other := &Env{PathDirs: env.PathDirs, Vars: env.Vars, Resolved: env.Resolved + ".1"}
	if env.Key() == other.Key() {
		t.Fatalf("two ambient toolchains share the key %q, so a tool built by the older one is reused", env.Key())
	}
}

// What a provisioned toolchain records is what got installed, not what the
// manifest asked for. A channel names a moving target: recording "stable"
// describes every Rust release there has ever been, so a baseline's producer
// and a tool cache's key both stop distinguishing the toolchains they exist to
// distinguish.
func TestProvisionedToolchainRecordsTheVersionItInstalled(t *testing.T) {
	dir := rustCrate(t, "stable")
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")
	marker := fakeRustupInstalling(t, bin, "1.91.0-x86_64-unknown-linux-gnu", false)

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.Rust, Overrides{}, &log)

	if got := installs(t, marker); !strings.Contains(got, "stable") {
		t.Fatalf("the declared channel must be installed, rustup was asked %q; log was %q", got, log.String())
	}
	if got := env.Version(); got != "1.91.0-x86_64-unknown-linux-gnu" {
		t.Fatalf("Version = %q, want the toolchain rustup installed rather than the channel the manifest names", got)
	}
}

// The bug-fix property itself. Two runners resolving one declaration to one
// toolchain — one that already had it, one that had to install it — describe it
// identically, so the baseline written by either compares against the other and
// a tool cached by either is reused by the other.
func TestAmbientAndProvisionedAgreeOnTheToolchainTheyResolvedTo(t *testing.T) {
	const active = "1.91.0-x86_64-unknown-linux-gnu"

	resolved := func(t *testing.T, components []string) *Env {
		t.Helper()
		dir := rustCrate(t, "stable")
		bin := fakeToolchainBin(t)
		fakeCargo(t, bin, "1.90.0")
		if components == nil {
			fakeRustupInstalling(t, bin, active, false)
		} else {
			fakeRustup(t, bin, active, components)
		}
		return ensureOne(t, dir, runner.Rust, Overrides{}, &bytes.Buffer{})
	}

	ambient := resolved(t, []string{"clippy", "rustfmt"})
	provisioned := resolved(t, nil)

	if ambient.Version() != provisioned.Version() {
		t.Fatalf("the same toolchain is recorded as %q when it was already there and %q when it was installed",
			ambient.Version(), provisioned.Version())
	}
	if ambient.Key() != provisioned.Key() {
		t.Fatalf("the same toolchain keys as %q ambient and %q provisioned, so nothing cached under one is reused under the other",
			ambient.Key(), provisioned.Key())
	}
}

// A confirming probe that fails after a successful install leaves a toolchain
// that is genuinely there. Discarding the environment over it would undo the
// install; recording a version nothing confirmed would be a claim. So the
// environment stands, the declaration is recorded as the stand-in it is, and
// the line says the identity is unconfirmed.
func TestAnUnconfirmedInstalledVersionKeepsTheEnvironmentAndWarns(t *testing.T) {
	dir := rustCrate(t, "1.85")
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")
	fakeRustupInstalling(t, bin, "1.91.0-x86_64-unknown-linux-gnu", true)

	var log bytes.Buffer
	// Overridden, so provisioning contributes a variable there is something to
	// lose: RUSTUP_TOOLCHAIN is what selects the channel just installed.
	env := ensureOne(t, dir, runner.Rust, Overrides{Rust: "stable"}, &log)

	if !slices.Contains(env.Environ(), "RUSTUP_TOOLCHAIN=stable") {
		t.Fatalf("Environ = %q, want the selection the successful install made", env.Environ())
	}
	if got := env.Version(); got != "stable" {
		t.Errorf("Version = %q, want the declaration as the stand-in for a version nothing confirmed", got)
	}
	out := log.String()
	if !strings.Contains(out, "installed toolchain stable") {
		t.Errorf("log should still report the install that succeeded, got %q", out)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, `recording "stable"`) {
		t.Errorf("log should warn that the installed version went unconfirmed and say what it recorded, got %q", out)
	}
}

// The confirming re-probe's first question is rustReady's own first question:
// whether rustup is on PATH at all. A rustup that answers everything else and
// then removes itself the moment it is asked to install something reaches
// exactly that branch on the re-probe, which is otherwise only exercised by
// the readiness check before anything is provisioned.
func TestConfirmFallsBackWhenRustupVanishesAfterInstalling(t *testing.T) {
	dir := unpinnedRustCrate(t)
	bin := fakeToolchainBin(t)
	marker := filepath.Join(t.TempDir(), "installs")
	writeScript(t, filepath.Join(bin, "rustup"), strings.Join([]string{
		"#!/bin/sh",
		`case "$1 $2" in`,
		`"show active-toolchain") echo 'nightly-x86_64-unknown-linux-gnu' ;;`,
		`"component list") exit 1 ;;`,
		`"toolchain install") echo "$*" >> ` + quote(marker) + `; /bin/rm -- "$0" ;;`,
		"*) exit 1 ;;",
		"esac",
		"",
	}, "\n"))

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.Rust, Overrides{}, &log)
	if env == nil {
		t.Fatalf("Ensure returned no environment after a successful install; log was %q", log.String())
	}
	if got := env.Version(); got != "an unidentified version" {
		t.Errorf("Version = %q, want the unpinned fallback recorded once rustup could no longer confirm anything", got)
	}
	out := log.String()
	if !strings.Contains(out, "warning:") {
		t.Errorf("log should warn that the installed version could not be confirmed once rustup vanished, got %q", out)
	}
	if strings.Contains(out, `recording "lydite"`) {
		t.Errorf("a fallback string must never be confused for a real toolchain name, got %q", out)
	}
}

// Installing is not selecting, and the confirming probe has to ask about the
// toolchain that was selected. An override lives only in .lydite/config.yml, so
// RUSTUP_TOOLCHAIN is the whole of the selection — asking rustup without it
// answers about the channel the directory would have resolved to instead.
func TestTheInstalledVersionIsConfirmedUnderTheSelectionProvisioningMade(t *testing.T) {
	dir := rustCrate(t, "1.85")
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")
	fakeRustupInstalling(t, bin, "1.90.0-x86_64-unknown-linux-gnu", false)

	env := ensureOne(t, dir, runner.Rust, Overrides{Rust: "stable"}, &bytes.Buffer{})

	if got := env.Version(); got != "stable-x86_64-unknown-linux-gnu" {
		t.Fatalf("Version = %q, want the toolchain RUSTUP_TOOLCHAIN selects", got)
	}
}

// Go's identity comes from the toolchain GOTOOLCHAIN selects, not from the one
// that was ambient a moment ago — so the confirming probe runs under the
// variables provisioning set, and those win over the probe's own.
func TestProvisionedGoRecordsTheToolchainItSelected(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	bin := fakeToolchainBin(t)
	// A go old enough to need the declared toolchain and new enough to fetch
	// it itself, reporting whichever toolchain GOTOOLCHAIN names — which is
	// what the real go command does.
	writeScript(t, filepath.Join(bin, "go"), strings.Join([]string{
		"#!/bin/sh",
		`case "${GOTOOLCHAIN}" in local|"") v=go1.21.5 ;; *) v="${GOTOOLCHAIN}" ;; esac`,
		`echo "go version $v linux/amd64"`,
		"",
	}, "\n"))

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.Go, Overrides{}, &log)

	if !slices.Contains(env.Environ(), "GOTOOLCHAIN=go1.26.0") {
		t.Fatalf("Environ = %q, want the declared toolchain pinned; log was %q", env.Environ(), log.String())
	}
	if got := env.Version(); got != "1.26.0" {
		t.Fatalf("Version = %q, want the toolchain GOTOOLCHAIN selected rather than the %q go.mod declares", got, "1.26")
	}
}

// The confirming probe runs the toolchain provisioning installed, which is
// reachable only through the directories provisioning put in front of PATH —
// Node is downloaded and unpacked into one of those and exists nowhere else.
func TestProbeUnderRunsTheToolchainOnlyTheProvisionedPathHas(t *testing.T) {
	fakeToolchainBin(t)
	unpacked := t.TempDir()
	writeScript(t, filepath.Join(unpacked, "node"), "#!/bin/sh\necho v22.21.1\n")
	p := probes[runner.TypeScript]

	got, err := probeUnder(context.Background(), p, &step{pathDirs: []string{unpacked}}, t.TempDir())
	if err != nil || got != "v22.21.1" {
		t.Fatalf("probeUnder = (%q, %v), want the version the unpacked toolchain reports", got, err)
	}

	// Without those directories there is nothing to run, and that is an error
	// rather than an empty version quietly recorded as one.
	if got, err := probeUnder(context.Background(), p, &step{}, t.TempDir()); err == nil {
		t.Fatalf("probeUnder off the provisioned PATH = %q, want an error", got)
	}
}

// A toolchain that runs but will not say what it is confirms nothing, and an
// empty version recorded as the answer is indistinguishable from one nothing
// probed for.
func TestProbeUnderRejectsAToolchainThatWillNotIdentifyItself(t *testing.T) {
	fakeToolchainBin(t)
	unpacked := t.TempDir()
	writeScript(t, filepath.Join(unpacked, "node"), "#!/bin/sh\necho ''\n")

	got, err := probeUnder(context.Background(), probes[runner.TypeScript],
		&step{pathDirs: []string{unpacked}}, t.TempDir())
	if err == nil {
		t.Fatalf("probeUnder = %q, want an error from a toolchain that names no version", got)
	}
}
