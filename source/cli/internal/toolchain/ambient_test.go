package toolchain

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/runner"
)

// fakeToolchainBin is a directory holding stand-in rustup and cargo
// executables, made the whole of PATH.
//
// Nothing in this package's tests may run the real rustup: provisioning would
// install a toolchain into the machine's shared ~/.rustup, and the Rust
// decision this file covers is precisely the one that decides whether that
// install happens.
func fakeToolchainBin(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	return bin
}

// fakeCargo puts a cargo on PATH that reports one version, the way a machine's
// rustup default channel would.
func fakeCargo(t *testing.T, bin, version string) {
	t.Helper()
	writeScript(t, filepath.Join(bin, "cargo"),
		fmt.Sprintf("#!/bin/sh\necho 'cargo %s (abcdef123 2025-01-01)'\n", version))
}

// fakeRustup puts a rustup on PATH that resolves every directory to active and
// reports components as installed on it, and returns the path of a file into
// which it records each `rustup toolchain install`. A nil components means the
// toolchain is not installed at all, which is how real rustup answers
// `component list` for a channel it has never fetched.
func fakeRustup(t *testing.T, bin, active string, components []string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "installs")
	show, list := "exit 1", "exit 1"
	if active != "" {
		show = "echo " + quote(active)
	}
	if components != nil {
		list = "printf '%s\\n'"
		for _, c := range components {
			list += " " + quote(c)
		}
	}
	writeScript(t, filepath.Join(bin, "rustup"), strings.Join([]string{
		"#!/bin/sh",
		`case "$1 $2" in`,
		`"show active-toolchain") ` + show + " ;;",
		`"component list") ` + list + " ;;",
		`"toolchain install") echo "$*" >> ` + quote(marker) + " ;;",
		"*) exit 1 ;;",
		"esac",
		"",
	}, "\n"))
	return marker
}

// fakeRustupInstalling puts a rustup on PATH whose answers change once it has
// been asked to install something: before that the channel resolves but is
// missing clippy, after it the channel is complete. A fake with fixed answers
// cannot reach the state the provisioning path leaves behind, which is the
// state the version recorded in Env.Resolved is read from.
//
// Like the real one it honours RUSTUP_TOOLCHAIN over the directory, and names
// the channel it selects target-qualified.
//
// showFailsAfterInstall makes `show active-toolchain` start failing once the
// install has run — the transient error the confirming probe has to survive
// without discarding an environment that was genuinely provisioned.
func fakeRustupInstalling(t *testing.T, bin, active string, showFailsAfterInstall bool) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "installs")
	guard := ""
	if showFailsAfterInstall {
		guard = "[ -f " + quote(marker) + " ] && exit 1; "
	}
	writeScript(t, filepath.Join(bin, "rustup"), strings.Join([]string{
		"#!/bin/sh",
		`case "$1 $2" in`,
		`"show active-toolchain") ` + guard +
			`case "${RUSTUP_TOOLCHAIN}" in "") echo ` + quote(active) +
			`;; *) echo "${RUSTUP_TOOLCHAIN}-x86_64-unknown-linux-gnu" ;; esac ;;`,
		`"component list")`,
		`  if [ -f ` + quote(marker) + ` ]; then printf '%s\n' clippy rustfmt; else printf '%s\n' rustfmt; fi ;;`,
		`"toolchain install") echo "$*" >> ` + quote(marker) + " ;;",
		"*) exit 1 ;;",
		"esac",
		"",
	}, "\n"))
	return marker
}

// installs is what the fake rustup was asked to install, empty when it was
// never asked.
func installs(t *testing.T, marker string) string {
	t.Helper()
	data, err := os.ReadFile(marker) // #nosec G304 -- a path this test just created
	if err != nil {
		return ""
	}
	return string(data)
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { // #nosec G306 -- a stand-in executable, in a temp dir
		t.Fatalf("write %s: %v", path, err)
	}
}

// quote renders a literal for the fake shell scripts above.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// rustCrate is a component directory pinning one channel.
func rustCrate(t *testing.T, channel string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "Cargo.toml", "[package]\nname = \"x\"\n")
	write(t, dir, "rust-toolchain.toml", "[toolchain]\nchannel = \""+channel+"\"\n")
	return dir
}

// unpinnedRustCrate is a component directory with no rust-toolchain.toml, no
// rust-toolchain file, and no config override — the case where rustup itself
// picks the channel, from its own default rather than from anything lydite
// declared.
func unpinnedRustCrate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "Cargo.toml", "[package]\nname = \"x\"\n")
	return dir
}

// An unpinned component has no rust-toolchain.toml for rustup to read, so
// installing a channel does nothing to select it: only RUSTUP_TOOLCHAIN or the
// file rustup already resolves to changes what a later cargo invocation picks.
// Provisioning the wrong channel — a hardcoded "stable" rather than the one
// rustup's own default already names — installs clippy and rustfmt onto a
// toolchain nothing will ever run under, and the component keeps failing under
// its original, incomplete default.
func TestRustProvisionsTheActiveChannelWhenNoneIsDeclared(t *testing.T) {
	dir := unpinnedRustCrate(t)
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")
	marker := fakeRustup(t, bin, "nightly-x86_64-unknown-linux-gnu",
		[]string{"rustfmt-x86_64-unknown-linux-gnu"})

	var log bytes.Buffer
	ensureOne(t, dir, runner.Rust, Overrides{}, &log)

	got := installs(t, marker)
	if !strings.Contains(got, "nightly-x86_64-unknown-linux-gnu") {
		t.Fatalf("rustup should be asked to install the channel it already resolves this directory to, got %q; log was %q", got, log.String())
	}
	if strings.Contains(got, "install stable") {
		t.Fatalf("rustup must not be asked to install a hardcoded stable when the component declared nothing, got %q", got)
	}
}

// The headline case. A crate pinning a channel *older* than the machine's
// rustup default is exactly what a version comparison gets wrong: `cargo
// --version`, run from lydite's own directory, reports the default, and 1.90
// clears a declared 1.85 by number. The channel then goes unprovisioned and
// rustup fetches it lazily in the middle of `cargo clippy`, without clippy —
// which surfaces as a failed check rather than as a setup step.
func TestRustProvisionsAChannelOlderThanTheMachineDefault(t *testing.T) {
	dir := rustCrate(t, "1.85")
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")
	marker := fakeRustup(t, bin, "1.85.0-x86_64-unknown-linux-gnu",
		[]string{"cargo-x86_64-unknown-linux-gnu", "rustfmt-x86_64-unknown-linux-gnu"})

	var log bytes.Buffer
	ensureOne(t, dir, runner.Rust, Overrides{}, &log)

	got := installs(t, marker)
	if !strings.Contains(got, "1.85") || !strings.Contains(got, "clippy") {
		t.Fatalf("the declared channel must be installed with clippy, rustup was asked %q; log was %q", got, log.String())
	}
}

// fakeRustupOverrideAware puts a rustup on PATH that resolves to the
// directory's own default channel unless RUSTUP_TOOLCHAIN names another,
// exactly as the real rustup does — and reports both as fully installed, so a
// test using it isolates the selection question from the readiness one.
func fakeRustupOverrideAware(t *testing.T, bin, def string) {
	t.Helper()
	writeScript(t, filepath.Join(bin, "rustup"), strings.Join([]string{
		"#!/bin/sh",
		`case "$1 $2" in`,
		`"show active-toolchain")`,
		`  case "${RUSTUP_TOOLCHAIN}" in`,
		`  "") echo ` + quote(def) + " ;;",
		`  *) echo "${RUSTUP_TOOLCHAIN}-x86_64-unknown-linux-gnu" ;;`,
		`  esac ;;`,
		`"component list") printf '%s\n' clippy-x86_64-unknown-linux-gnu rustfmt-x86_64-unknown-linux-gnu ;;`,
		`"toolchain install") exit 0 ;;`,
		"*) exit 1 ;;",
		"esac",
		"",
	}, "\n"))
}

// A config override selects a toolchain rustup's own directory-based
// resolution knows nothing about — it lives only in .lydite/config.yml — so
// asking readiness without RUSTUP_TOOLCHAIN set checks the directory's own
// default instead of the channel the override actually names. Both are fully
// installed here, so the bug this guards is not "provisioning never runs" —
// it is the override being silently ignored: a check would still run under
// the directory's default, and Env.Resolved would record that default as the
// baseline's producer instead of the channel a human asked for in config.
func TestRustOverrideSelectsEvenWhenBothChannelsAreReady(t *testing.T) {
	dir := unpinnedRustCrate(t)
	bin := fakeToolchainBin(t)
	fakeRustupOverrideAware(t, bin, "stable-x86_64-unknown-linux-gnu")

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.Rust, Overrides{Rust: "nightly"}, &log)
	if env == nil {
		t.Fatalf("Ensure returned no environment for an overridden Rust component; log was %q", log.String())
	}
	if !strings.Contains(env.Resolved, "nightly") {
		t.Fatalf("Resolved = %q, want the overridden channel, not the directory's own default; log was %q", env.Resolved, log.String())
	}
	if !slices.Contains(env.Vars, "RUSTUP_TOOLCHAIN=nightly") {
		t.Fatalf("Vars = %v, want RUSTUP_TOOLCHAIN=nightly carried into the environment so a later cargo invocation actually selects it", env.Vars)
	}
}

// A named channel has no version to compare at all, so rustup is the only
// thing that can say whether it is ready. When it is, lydite installs nothing.
func TestRustIsSatisfiedWhenRustupHasTheChannelWithBothComponents(t *testing.T) {
	dir := rustCrate(t, "stable")
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")
	marker := fakeRustup(t, bin, "stable-x86_64-unknown-linux-gnu", []string{
		"cargo-x86_64-unknown-linux-gnu",
		"clippy-x86_64-unknown-linux-gnu",
		"rustfmt-x86_64-unknown-linux-gnu",
	})

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.Rust, Overrides{}, &log)

	if got := installs(t, marker); got != "" {
		t.Fatalf("a ready channel must be installed by nobody, rustup was asked %q", got)
	}
	if !strings.Contains(log.String(), "using ambient") {
		t.Errorf("log should say the ambient toolchain was reused, got %q", log.String())
	}
	// The toolchain rustup selects, not the one `cargo --version` reports: it
	// is half of what measures a component's coverage, and a baseline records
	// what measured it.
	if got := env.Version(); got != "stable-x86_64-unknown-linux-gnu" {
		t.Errorf("Version = %q, want the toolchain rustup resolves this directory to", got)
	}
}

// cargo on PATH is not the question. Without rustup nothing can resolve a
// channel and nothing can be provisioned, so this must take the provisioning
// path — which then fails and warns — rather than read as satisfied.
func TestRustWithoutRustupIsNotSatisfiedByCargoAlone(t *testing.T) {
	dir := rustCrate(t, "stable")
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.Rust, Overrides{}, &log)

	if len(env.Environ()) != 0 {
		t.Fatalf("a failed provision must contribute nothing to the env, got %+v", env)
	}
	out := log.String()
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "rustup") {
		t.Fatalf("log should warn that rustup is missing, got %q", out)
	}
}

// A channel rustup resolves but has never fetched is not satisfied, however
// new it is.
func TestRustProvisionsAChannelThatIsNotInstalled(t *testing.T) {
	dir := rustCrate(t, "1.99")
	bin := fakeToolchainBin(t)
	fakeCargo(t, bin, "1.90.0")
	marker := fakeRustup(t, bin, "1.99.0-x86_64-unknown-linux-gnu", nil)

	var log bytes.Buffer
	ensureOne(t, dir, runner.Rust, Overrides{}, &log)

	if got := installs(t, marker); !strings.Contains(got, "1.99") {
		t.Fatalf("an uninstalled channel must be provisioned, rustup was asked %q", got)
	}
	if !strings.Contains(log.String(), "declared in rust-toolchain.toml") {
		t.Errorf("log should name the manifest the channel came from, got %q", log.String())
	}
}

// Installed is not ready. Either component missing sends the channel down the
// provisioning path, and the line says which one is missing — a scan that
// reported a pass here would fail in `cargo clippy` or `cargo fmt` instead.
func TestRustProvisionsWhenEitherComponentIsMissing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		components []string
		want       string
	}{
		{"no clippy", []string{"cargo-x86_64-unknown-linux-gnu", "rustfmt-x86_64-unknown-linux-gnu"}, "clippy"},
		{"no rustfmt", []string{"cargo-x86_64-unknown-linux-gnu", "clippy-x86_64-unknown-linux-gnu"}, "rustfmt"},
		{"neither", []string{"cargo-x86_64-unknown-linux-gnu"}, "clippy and rustfmt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := rustCrate(t, "stable")
			bin := fakeToolchainBin(t)
			fakeCargo(t, bin, "1.90.0")
			marker := fakeRustup(t, bin, "stable-x86_64-unknown-linux-gnu", tc.components)

			var log bytes.Buffer
			ensureOne(t, dir, runner.Rust, Overrides{Disabled: true}, &log)

			if got := installs(t, marker); got != "" {
				t.Fatalf("provisioning was disabled but rustup was asked %q", got)
			}
			if !strings.Contains(log.String(), "without "+tc.want) {
				t.Fatalf("log should name the missing component(s) %q, got %q", tc.want, log.String())
			}
		})
	}
}

// rustup lists components target-qualified on a toolchain with a host triple
// and bare on one without, and both spellings mean installed.
func TestRustReadyAcceptsBareComponentNames(t *testing.T) {
	dir := rustCrate(t, "stable")
	bin := fakeToolchainBin(t)
	marker := fakeRustup(t, bin, "my-toolchain", []string{"cargo", "clippy", "rustfmt"})

	ensureOne(t, dir, runner.Rust, Overrides{}, &bytes.Buffer{})

	if got := installs(t, marker); got != "" {
		t.Fatalf("bare component names mean installed, but rustup was asked %q", got)
	}
}

// rustup that cannot name an active toolchain for the directory answers
// nothing, and nothing is not satisfaction.
func TestRustReadyReportsWhatRustupIsShortOf(t *testing.T) {
	bin := fakeToolchainBin(t)
	dir := t.TempDir()

	if _, ready, lack := rustReady(context.Background(), dir, nil); ready || lack == "" {
		t.Fatalf("rustReady with no rustup = ready %v, lack %q; want not ready, with a reason", ready, lack)
	}

	fakeRustup(t, bin, "", nil)
	active, ready, lack := rustReady(context.Background(), dir, nil)
	if active != "" || ready || lack == "" {
		t.Fatalf("rustReady with an unresolvable channel = (%q, %v, %q); want ready false with a reason", active, ready, lack)
	}
}

// The toolchain rustup selects depends on the directory cargo runs in, so two
// components sharing a language and a declaration are still two questions.
func TestRustResolutionIsNotSharedAcrossComponentDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ready", "bare"} {
		write(t, filepath.Join(root, name), "Cargo.toml", "[package]\nname = \"x\"\n")
	}
	bin := fakeToolchainBin(t)
	// This rustup answers per directory: only `ready/` has clippy.
	marker := filepath.Join(t.TempDir(), "installs")
	writeScript(t, filepath.Join(bin, "rustup"), strings.Join([]string{
		"#!/bin/sh",
		`case "$1 $2" in`,
		`"show active-toolchain") echo stable-x86_64-unknown-linux-gnu ;;`,
		`"component list")`,
		`  case "$PWD" in *ready) printf '%s\n' clippy rustfmt ;; *) printf '%s\n' rustfmt ;; esac ;;`,
		`"toolchain install") echo "$*" >> ` + quote(marker) + " ;;",
		"*) exit 1 ;;",
		"esac",
		"",
	}, "\n"))

	var log bytes.Buffer
	envs, err := Ensure(context.Background(), root, []Unit{
		{Name: "ready", Lang: runner.Rust, Dir: "ready"},
		{Name: "bare", Lang: runner.Rust, Dir: "bare"},
	}, Overrides{}, &log)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := installs(t, marker); got == "" {
		t.Fatalf("the component without clippy must be provisioned on its own account, got envs %+v", envs)
	}
	// Neither crate has a rust-toolchain.toml, so there is no manifest to
	// name: an empty one in the line reads as a file lydite failed to find.
	if strings.Contains(log.String(), "declared in") {
		t.Errorf("nothing declared a channel, so the line must name no manifest, got %q", log.String())
	}
}
