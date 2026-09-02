// Package toolchain makes sure the language toolchain each detected
// ecosystem needs — the Go, Rust and Node runtimes themselves — is present at
// the version the repository declares, before any scanner runs.
//
// This closes the one hole in lydite's otherwise complete "pin the exact
// toolchain, don't reuse ambient installs" principle (see internal/golang).
// lydite provisions everything it *runs* — gosec and govulncheck via `go
// install` into a version-keyed cache, cargo-audit/cargo-deny via `cargo
// install`, Biome via npm, Semgrep via pipx — but until now it assumed the
// language toolchain it does that provisioning *with* was simply there. On a
// GitHub-hosted runner that holds, which is why nothing was visibly broken;
// on a self-hosted or container runner without Go it fails at `go install`,
// the version is whatever the image happens to ship rather than what the repo
// declares, and there is no shared module cache so every run re-downloads.
//
// Two rules shape the whole package:
//
//   - The version comes from what the repository already states — the `go`
//     and `toolchain` directives in every discovered go.mod, the channel in
//     rust-toolchain.toml, engines.node or .nvmrc. Never from .lydite/config.yml.
//     Those files are already authoritative and tool-enforced, so a second
//     copy in lydite's config could only agree redundantly or drift
//     silently, and a stale duplicate is worse than none because it reads as
//     authoritative. .lydite/config.yml can override, which is an explicit local
//     exception rather than a parallel source of truth.
//   - An ambient toolchain that already satisfies the declared version is
//     used as-is. Downloading a toolchain that is already present and correct
//     is pure cost, and on the runners lydite actually runs on today that is
//     the overwhelmingly common case — so the common path here does no
//     network I/O at all.
//
// Doing this in lydite rather than in each caller's CI is what makes it work
// for a monorepo. lydite already knows which ecosystems it detected and in
// which directories, so it reads every go.mod under the scan root rather than
// one at a fixed path — the mistake that made gt's short-lived `setup-go`
// step (19e4b77, reverted in a0ed107) a no-op for wardnet, whose modules live
// under wctl/ and sdk/wardnet-go/. It is also the only place that helps
// wardnet at all, since wardnet calls wardnet/bulwark@v1 directly rather than
// through gt.
package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"lydite/lydite/internal/runner"
)

// Overrides is the .lydite/config.yml-supplied part of toolchain resolution, passed
// in rather than imported so internal/config stays a leaf this package
// doesn't depend on.
type Overrides struct {
	// Disabled turns provisioning off entirely. Probing and reporting still
	// happen, so an air-gapped or fully-preprovisioned runner keeps the
	// diagnostics without the downloads.
	Disabled bool
	// Go, Rust and Node override the version read from the manifests. Empty
	// means "use what the repo declares", which is the intended state.
	Go, Rust, Node string
}

// For returns the override for one language, or "" if none is set.
func (o Overrides) For(e runner.Lang) string {
	switch e {
	case runner.Go:
		return o.Go
	case runner.Rust:
		return o.Rust
	case runner.TypeScript:
		return o.Node
	default:
		return ""
	}
}

// Env is the environment change needed to make one component's resolved
// toolchains usable: directories to prepend to PATH, and variables to set.
type Env struct {
	PathDirs []string
	Vars     []string
}

// Environ is the environment a child process running under this toolchain
// gets, ready to hand to executil.
func (e *Env) Environ() []string {
	if e == nil {
		return nil
	}
	return Compose(e.PathDirs, e.Vars)
}

// Compose builds a child process environment: the variables in order, and
// exactly one PATH entry holding dirs ahead of the current PATH.
//
// One PATH entry, and therefore one place that builds it, because a child's
// environment is a flat list where the last occurrence of a key wins. Two
// callers each prepending their own directories produce two PATH entries, of
// which one is silently discarded — so a run would provision a toolchain,
// prepend it, and then execute against the ambient one because a pinned
// tool's own entry came later. Nothing about that is visible in argv.
//
// Dirs are in final PATH order: the caller nearest the invocation goes first,
// and a directory a caller could not supply is simply absent rather than
// empty.
func Compose(dirs []string, vars ...[]string) []string {
	var out []string
	for _, v := range vars {
		out = append(out, v...)
	}
	dirs = nonEmpty(dirs)
	if len(dirs) == 0 {
		return out
	}
	// Prepended, not appended: a provisioned toolchain exists precisely
	// because the ambient one was missing or too old, so it has to win.
	//
	// The inherited PATH is filtered the same way the supplied directories
	// are. An empty one — a minimal container, an `env -i` invocation — would
	// otherwise leave a trailing separator, and an empty PATH element means
	// the current directory to a shell and to an exec lookup. Since a
	// component's commands run with their working directory set to a
	// directory of the repository being scanned, that puts the scanned
	// repository on the child's PATH.
	parts := nonEmpty(append(append([]string{}, dirs...), os.Getenv("PATH")))
	return append(out, "PATH="+strings.Join(parts, string(os.PathListSeparator)))
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Key identifies what this environment changes, for a caller that caches
// something built under it.
//
// Both halves matter, and the variables alone are not enough: a downloaded Go
// toolchain sets GOTOOLCHAIN=local — the same value an ambient one that
// already satisfies the declaration gets — and says which toolchain by putting
// a version-keyed directory on PATH. Two components that each downloaded a
// different Go would otherwise share a key, and a tool built under the first
// would be reused by the second, which is the failure the pin exists to
// prevent wearing a cache key.
func (e *Env) Key() string {
	if e == nil || (len(e.PathDirs) == 0 && len(e.Vars) == 0) {
		return "ambient"
	}
	h := sha256.New()
	for _, d := range e.PathDirs {
		_, _ = io.WriteString(h, d+"\x00")
	}
	_, _ = io.WriteString(h, "\x00")
	for _, v := range e.Vars {
		_, _ = io.WriteString(h, v+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Envs is one Env per component, keyed by component name. A component with
// nothing to apply has no entry, and For returns nil for it.
type Envs map[string]*Env

// For returns the environment for one component.
func (e Envs) For(component string) *Env {
	if e == nil {
		return nil
	}
	return e[component]
}

// Ensure resolves, and where necessary provisions, the language toolchain
// each unit needs, returning one environment per component.
//
// Per component rather than per repository, because that is the only unit at
// which the question has one answer: a workspace requiring Node 22 and a tools
// package pinning 18 are two components, and a single process environment can
// hold one of them. The environment is returned rather than applied for the
// same reason — components run concurrently in one process, so a toolchain
// written into that process is one every other component inherits.
//
// Units resolving to the same requirement are probed and provisioned once and
// share the result, so two Go components on one version cost one diagnostic
// line rather than two identical ones.
//
// Provisioning failures are reported to w and do not fail the run. That is a
// deliberate asymmetry with the rest of lydite's gates: this step is
// preparation, not a check, and falling through to "whatever is on PATH" is
// exactly what a run without it does. Turning a working scan on a
// GitHub-hosted runner into a hard failure because a toolchain download hit a
// network blip would be a regression, and if the toolchain really is absent
// the very next step fails loudly and specifically ("cargo: executable file
// not found"). What must not happen is failing silently, so every skip,
// substitution and failure is named on w.
func Ensure(ctx context.Context, root string, units []Unit, ov Overrides, w io.Writer) (Envs, error) {
	reqs, err := Requirements(root, units, ov)
	if err != nil {
		return nil, err
	}

	envs := Envs{}
	// Keyed by what decides the answer — the language and the version asked
	// for — so a second component wanting the same toolchain reuses the first
	// one's result instead of probing the machine again.
	shared := map[string]*Env{}
	for _, req := range reqs {
		key := string(req.Lang) + "\x00" + req.Version + "\x00" + req.Raw
		env, seen := shared[key]
		if !seen {
			resolved, err := resolveOne(ctx, req, ov, w)
			if err != nil {
				return nil, err
			}
			env = resolved
			shared[key] = env
		}
		// Only when there is something to apply, so a component with nothing
		// to add has no entry rather than a nil one — For answers the same
		// either way, and two shapes for one state is one more than the map
		// needs.
		if env != nil {
			envs[req.Unit.Name] = env
		}
	}
	return envs, nil
}

// resolveOne probes for one requirement's toolchain and provisions it if what
// is present does not satisfy it, returning the environment that makes the
// result usable — or nil when there is nothing to apply.
func resolveOne(ctx context.Context, req Requirement, ov Overrides, w io.Writer) (*Env, error) {
	p, ok := probes[req.Lang]
	if !ok {
		return nil, nil
	}
	ambient, present := installed(ctx, p)

	if satisfied(req, ambient, present) {
		if err := logf(w, "%s: using ambient %s %s (%s)\n",
			req.Lang, p.bin, display(ambient), declaredBy(req)); err != nil {
			return nil, err
		}
		// Satisfied is not the same as nothing to do. Go still needs its
		// toolchain pinned so that installing an external tool cannot
		// silently switch away from the version just verified — see
		// pinAmbientGo. This costs no download and is why the branch
		// doesn't simply return nil.
		if req.Lang == runner.Go {
			if st := pinAmbientGo(req); st != nil {
				if err := logf(w, "%s\n", st.note); err != nil {
					return nil, err
				}
				return &Env{Vars: st.vars}, nil
			}
		}
		return nil, nil
	}
	if ov.Disabled {
		if err := logf(w, "warning: %s toolchain %s, and toolchain.enabled is false — continuing with what is on PATH\n",
			req.Lang, shortfall(req, ambient, present)); err != nil {
			return nil, err
		}
		return nil, nil
	}

	st, err := provision(ctx, req, ambient, present)
	if err != nil {
		if logErr := logf(w, "warning: could not provision the %s toolchain (%s): %v — continuing with what is on PATH\n",
			req.Lang, shortfall(req, ambient, present), err); logErr != nil {
			return nil, logErr
		}
		return nil, nil
	}
	if st == nil {
		return nil, nil
	}
	for _, dir := range st.pathDirs {
		ensureExecutable(dir)
	}
	if st.note != "" {
		if err := logf(w, "%s\n", st.note); err != nil {
			return nil, err
		}
	}
	return &Env{PathDirs: st.pathDirs, Vars: st.vars}, nil
}

// provision dispatches to the per-language provisioner. Each one differs in
// kind, not just in URL: Go delegates to its own GOTOOLCHAIN mechanism, Rust
// delegates to rustup, and only Node is downloaded and unpacked by lydite.
func provision(ctx context.Context, req Requirement, ambient string, present bool) (*step, error) {
	switch req.Lang {
	case runner.Go:
		return provisionGo(ctx, req, ambient, present)
	case runner.Rust:
		return provisionRust(ctx, req, ambient, present)
	case runner.TypeScript:
		return provisionNode(ctx, req, ambient, present)
	default:
		return nil, nil
	}
}

// satisfied reports whether the ambient toolchain is good enough to use
// as-is. An unpinned requirement is satisfied by any present toolchain — the
// repo named no floor, so there is nothing to be too old for.
func satisfied(req Requirement, ambient string, present bool) bool {
	if !present {
		return false
	}
	if req.Unpinned() {
		return true
	}
	return !olderThan(ambient, req.Version)
}

// declaredBy renders the requirement side of a message as a complete
// parenthetical, because the three cases don't share a sentence shape: a
// pinned version is something the ambient toolchain *satisfies*, a named
// channel is something it merely *matches in kind*, and an absent
// declaration is the notable fact all by itself — that last one is worth
// saying out loud rather than papering over, since "no version declared" is
// often a gap the reader can go close.
func declaredBy(req Requirement) string {
	if req.Unpinned() {
		if req.Raw != "" {
			return fmt.Sprintf("%s declares the %q channel", req.Source, req.Raw)
		}
		// Named, because with one toolchain per component "nothing is
		// declared" is a statement about one directory rather than about the
		// repository — and for Go it is also the reason GOTOOLCHAIN goes
		// unpinned, which is otherwise the quietest thing this package does.
		if req.Unit.Name != "" {
			return fmt.Sprintf("component %s declares no version", req.Unit.Name)
		}
		return "no version declared by this repo"
	}
	return fmt.Sprintf("satisfies %s from %s", displayRaw(req), req.Source)
}

// shortfall renders why the ambient toolchain was not good enough.
func shortfall(req Requirement, ambient string, present bool) string {
	if !present {
		return "is not installed"
	}
	if req.Unpinned() {
		return "is unusable"
	}
	return fmt.Sprintf("is %s, older than the declared %s", display(ambient), displayRaw(req))
}

func logf(w io.Writer, format string, args ...any) error {
	if w == nil {
		return nil
	}
	_, err := fmt.Fprintf(w, format, args...)
	return err
}
