package toolchain

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/runner"
)

// probe is how one ecosystem's already-installed language toolchain is found
// and identified. Split out as a struct so the decision logic in Ensure is
// one code path across all three, and so tests can substitute a probe without
// having a real toolchain on the machine.
type probe struct {
	// bin is the command whose presence means "this toolchain is installed".
	bin string
	// versionArgs asks that command to identify itself.
	versionArgs []string
	// env is added to the probe's environment. Only Go needs it; see below.
	env []string
	// parse extracts a canonical version from the command's output.
	parse func(output string) string
}

var probes = map[runner.Lang]probe{
	runner.Go: {
		bin:         "go",
		versionArgs: []string{"version"},
		// GOTOOLCHAIN=local is load-bearing, not tidiness. `go version` run
		// inside a module honours that module's `toolchain` directive and
		// reports the version it would switch *to*, downloading it if
		// necessary. Probing that way measures a toolchain that is not the
		// one `GOTOOLCHAIN=local` would subsequently select, so lydite would
		// conclude "ambient already satisfies the declaration", pin `local`,
		// and land on an older toolchain than the one it just measured — the
		// two answers have to come from the same place. Probing with `local`
		// reports the genuinely installed toolchain, which is exactly what
		// the pin will use.
		env: []string{"GOTOOLCHAIN=local"},
		// "go version go1.26.5 linux/amd64"
		parse: func(out string) string { return fieldAfter(out, "version") },
	},
	runner.Rust: {
		// cargo, not rustc: every Rust check and the coverage path invoke
		// cargo. This names the binary lydite runs, for the diagnostic line;
		// whether a Rust component is satisfied is rustReady's answer, not
		// this version's.
		bin:         "cargo",
		versionArgs: []string{"--version"},
		// "cargo 1.96.0 (abcdef123 2025-01-01)"
		parse: func(out string) string { return nthField(out, 1) },
	},
	runner.TypeScript: {
		bin:         "node",
		versionArgs: []string{"--version"},
		// "v22.21.1"
		parse: func(out string) string { return nthField(out, 0) },
	},
}

// installed reports the canonical version of the ambient toolchain for an
// ecosystem, and whether one is present at all.
//
// "Present but unidentifiable" is reported as present with an empty version,
// which olderThan then treats as older than any requirement. That is the
// conservative reading: a toolchain that won't say what it is can't be shown
// to satisfy a pin.
func installed(ctx context.Context, p probe) (version string, present bool) {
	if _, err := exec.LookPath(p.bin); err != nil {
		return "", false
	}
	// Deliberately not executil.Run: that streams the child's output to the
	// terminal, and `go version` chatter ahead of every scan is noise. This
	// is a probe, not a check whose output the user wants.
	cmd := exec.CommandContext(ctx, p.bin, p.versionArgs...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- p.bin/versionArgs come from this file's own static probes table, not user input
	if len(p.env) > 0 {
		cmd.Env = append(os.Environ(), p.env...)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", true
	}
	return p.parse(string(out)), true
}

// probeUnder asks the toolchain a provisioning step has just made available
// what version it is, under the environment that step produced: its
// directories in front of PATH, its variables applied, and the component's own
// directory as the working directory.
//
// Probing any other way answers about a different toolchain. A downloaded Go
// or Node lives only in a directory the step contributes, and Go's version
// depends on the GOTOOLCHAIN the step sets — so installed's answer, taken
// before any of that existed, is the ambient toolchain the step was run to
// replace.
//
// The step's variables go after the probe's own, because a flat environment
// resolves the last occurrence of a key: GOTOOLCHAIN=local identifies what is
// installed *now*, and the step's pin is what the environment will actually
// select.
func probeUnder(ctx context.Context, p probe, st *step, dir string) (string, error) {
	env := Compose(st.pathDirs, nil, p.env, st.vars)
	bin, err := lookPathIn(env, p.bin)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, p.versionArgs...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- versionArgs come from this file's own static probes table, and bin is that table's name resolved against a PATH this package built
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	version := p.parse(string(out))
	if version == "" {
		return "", fmt.Errorf("%s does not name a version", p.bin)
	}
	return version, nil
}

// lookPathIn resolves a command against the PATH a composed environment
// carries, rather than against this process's own.
//
// exec.LookPath cannot do it: a child's PATH decides nothing about how its
// argv[0] is resolved, which happens in the parent. So a toolchain lydite has
// just unpacked — reachable only through a directory the step contributes — is
// invisible to a lookup that consults the ambient PATH, and the probe would
// silently run the ambient toolchain instead of the provisioned one.
func lookPathIn(env []string, bin string) (string, error) {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, kv := range env {
		// The last PATH wins, the same way it does in the child.
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			dirs = filepath.SplitList(v)
		}
	}
	for _, d := range dirs {
		candidate := filepath.Join(d, bin)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: executable file not found in the provisioned PATH", bin)
}

// rustChecksNeed are the rustup components lydite's Rust checks run: clippy
// for the lint gate, rustfmt for the formatting one. A channel installed
// without them is not a channel a scan can use.
var rustChecksNeed = []string{"clippy", "rustfmt"}

// rustReady asks rustup, from the component's own directory, whether a cargo
// invocation there already has what every Rust check needs: the channel that
// directory resolves to, installed, carrying clippy and rustfmt. It returns
// the toolchain's name, that verdict, and — when the verdict is no — what it
// is short of, phrased to complete "the rust toolchain ...".
//
// A version comparison against `cargo --version` cannot answer this. rustup
// selects a toolchain per invocation by walking up from the working directory
// for a rust-toolchain.toml, and lydite's own directory has none, so that
// probe reports the machine's rustup *default* channel rather than the
// crate's. A component pinning an older channel than the default then reads as
// satisfied and is never provisioned; rustup fetches it lazily in the middle
// of `cargo clippy`, without clippy, and the gap surfaces as a failed check
// instead of a setup step. Named channels ("stable", "nightly") carry no
// version to compare at all, so rustup resolving the file itself is the only
// answer there is.
//
// No rustup means nothing can be resolved and nothing can be provisioned, and
// that is reported as not ready rather than as satisfied: the caller takes the
// provisioning path, which fails and warns, instead of reporting a pass it
// never established.
// env is added to rustup's own environment, because the selection is part of
// the question: a channel chosen by RUSTUP_TOOLCHAIN is one rustup names only
// when it is asked with that variable set, and asking without it reports the
// channel the directory would have resolved to instead.
func rustReady(ctx context.Context, dir string, env []string) (active string, ready bool, lack string) {
	if _, err := exec.LookPath("rustup"); err != nil {
		return "", false, "needs rustup, which is not installed"
	}
	out, err := rustupIn(ctx, dir, env, "show", "active-toolchain")
	if err != nil {
		return "", false, "is one rustup resolves no channel for in this component"
	}
	// "1.85.0-aarch64-apple-darwin (overridden by '/repo/rust-toolchain.toml')"
	// — the name is the first field, kept verbatim rather than canonicalised,
	// because a channel is not a version and the host triple is what tells two
	// installs of one channel apart.
	active = firstField(out)
	if active == "" {
		return "", false, "is one rustup resolves no channel for in this component"
	}
	out, err = rustupIn(ctx, dir, env, "component", "list", "--installed", "--toolchain", active)
	if err != nil {
		return active, false, "resolves to " + active + ", which is not installed"
	}
	var missing []string
	for _, want := range rustChecksNeed {
		if !hasComponent(out, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return active, false, "resolves to " + active + ", which is installed without " + strings.Join(missing, " and ")
	}
	return active, true, ""
}

// rustupIn runs rustup with its working directory set to the component's,
// which is the whole point: rustup picks a toolchain per invocation by walking
// up from that directory, so an answer taken anywhere else is an answer about
// a different toolchain.
//
// Deliberately not executil.Run, for the same reason installed is not: this is
// a probe, and rustup chatter ahead of every scan is noise.
func rustupIn(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "rustup", args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- args are this file's own literals plus a toolchain name rustup itself just printed
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	return string(out), err
}

// hasComponent reports whether `rustup component list --installed` named one.
// Components are listed target-qualified ("clippy-aarch64-apple-darwin") on a
// toolchain carrying a host triple and bare ("clippy") on one that does not,
// and both spellings mean installed.
func hasComponent(list, name string) bool {
	for line := range strings.SplitSeq(list, "\n") {
		line = strings.TrimSpace(line)
		if line == name || strings.HasPrefix(line, name+"-") {
			return true
		}
	}
	return false
}

// firstField returns the first whitespace-separated field, verbatim.
func firstField(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// fieldAfter returns the whitespace-separated field following the first
// occurrence of key, canonicalised.
func fieldAfter(out, key string) string {
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == key && i+1 < len(fields) {
			return canonical(fields[i+1])
		}
	}
	return ""
}

// nthField returns the nth whitespace-separated field, canonicalised.
func nthField(out string, n int) string {
	fields := strings.Fields(out)
	if n >= len(fields) {
		return ""
	}
	return canonical(fields[n])
}
