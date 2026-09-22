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
// under wctl/ and sdk/wardnet-go/.
package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	// Resolved names the toolchain this environment selects. It changes
	// nothing in the child; it is read by Key and by Version.
	//
	// Key needs it because an ambient toolchain that already satisfies the
	// declaration contributes no directory and only GOTOOLCHAIN=local, so
	// without it every such component on every machine hashes to one value: a
	// CI image whose Go is bumped 1.25 to 1.26 keeps reusing a tool built by
	// 1.25, which rejects 1.26 source outright.
	//
	// Version needs it because the toolchain is part of what measured a
	// component's coverage — it writes Go's profile outright, and its LLVM
	// writes the line records in Rust's lcov — and a baseline records what
	// measured it.
	Resolved string
}

// Version names the toolchain this environment selects, or nothing when there
// is no environment at all.
//
// Nil-safe like Environ, because a component whose language lydite has no
// probe for is handed no environment, and the answer for one is the same as
// the answer for a toolchain that would not identify itself: unknown.
func (e *Env) Version() string {
	if e == nil {
		return ""
	}
	return e.Resolved
}

// Environ is the environment a child process running under this toolchain
// gets, ready to hand to executil.
func (e *Env) Environ() []string {
	if e == nil {
		return nil
	}
	return Compose(e.PathDirs, nil, e.Vars)
}

// Compose builds a child process environment: the variables in order, and
// exactly one PATH entry holding leading ahead of the current PATH and
// trailing behind it.
//
// One PATH entry, and therefore one place that builds it, because a child's
// environment is a flat list where the last occurrence of a key wins. Two
// callers each prepending their own directories produce two PATH entries, of
// which one is silently discarded — so a run would provision a toolchain,
// prepend it, and then execute against the ambient one because a pinned
// tool's own entry came later. Nothing about that is visible in argv.
//
// Leading dirs are in final PATH order: the caller nearest the invocation goes
// first. Trailing is for directories that must not shadow anything already
// present — a path a scanned repository asked for, which may add a binary and
// may not replace one lydite resolved.
func Compose(leading, trailing []string, vars ...[]string) []string {
	var out []string
	for _, v := range vars {
		out = append(out, v...)
	}
	leading, trailing = nonEmpty(leading), nonEmpty(trailing)
	if len(leading) == 0 && len(trailing) == 0 {
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
	parts := append([]string{}, leading...)
	// Every element of the inherited PATH, not the variable as a whole: a
	// PATH of "/usr/bin::/bin" carries an empty element, and an empty element
	// means the current directory — which for a component's commands is a
	// directory of the repository being scanned.
	parts = append(parts, nonEmpty(filepath.SplitList(os.Getenv("PATH")))...)
	parts = append(parts, trailing...)
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
	if e == nil || (len(e.PathDirs) == 0 && len(e.Vars) == 0 && e.Resolved == "") {
		return "ambient"
	}
	h := sha256.New()
	_, _ = io.WriteString(h, e.Resolved+"\x00")
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
	// The work is shared and the diagnostic is not. Keyed by what decides the
	// answer — the language and the version asked for — so a second component
	// wanting the same toolchain reuses the first one's result rather than
	// probing the machine again; but the line it prints is written per
	// component, because "component api declares no version" is a statement
	// about one directory. Logging inside the shared step named whichever
	// component came first and left the rest unpinned in silence.
	shared := map[string]*resolution{}
	for _, req := range reqs {
		key := string(req.Lang) + "\x00" + req.Version + "\x00" + req.Raw
		// Rust's answer is per directory, not per channel. rustup resolves a
		// toolchain by walking up from the directory cargo runs in, so two
		// components declaring the same thing — or declaring nothing — can
		// still select different toolchains, and sharing one answer between
		// them would hand the second component the first one's directory.
		if req.Lang == runner.Rust {
			key += "\x00" + req.Unit.Dir
		}
		got, seen := shared[key]
		if !seen {
			resolved, err := resolveOne(ctx, root, req, ov)
			if err != nil {
				return nil, err
			}
			got = &resolved
			shared[key] = got
		}
		if err := got.log(w, req); err != nil {
			return nil, err
		}
		env := got.env
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

// resolution is what one requirement resolved to: the environment to apply,
// and enough of how it got there to write the line each component that shares
// it needs. The message is not stored, because it names the component and the
// manifest, and those differ between the components sharing this.
type resolution struct {
	env *Env
	// kind is which branch was taken.
	kind resolutionKind
	// bin is the probed command's name, ambient its version, present whether
	// it was there at all.
	bin, ambient string
	present      bool
	// lack names the shortfall when the language has a more specific answer
	// than a version comparison. Rust does: a channel can be installed and
	// still be missing the clippy or rustfmt component every check runs, which
	// neither "is older than" nor "is not installed" describes.
	lack string
	// note is the provisioning step's own sentence, which is about the
	// machine rather than about any component, so it is shared verbatim.
	note string
	// noted guards the shared note, so a step that installed something says
	// so once rather than once per component that reuses it.
	noted bool
}

type resolutionKind int

const (
	// resolutionNone is a language with no probe, so nothing to say.
	resolutionNone resolutionKind = iota
	resolutionAmbient
	resolutionDisabled
	resolutionFailed
	resolutionProvisioned
)

// log writes the line one component needs about this resolution.
func (r *resolution) log(w io.Writer, req Requirement) error {
	switch r.kind {
	case resolutionNone:
		return nil
	case resolutionAmbient:
		if err := logf(w, "%s: using ambient %s %s (%s)\n",
			req.Lang, r.bin, display(r.ambient), declaredBy(req)); err != nil {
			return err
		}
		// The pin is about the machine rather than any one component, so it
		// is said once however many components share this toolchain.
		if r.note == "" || r.noted {
			return nil
		}
		r.noted = true
		return logf(w, "%s\n", r.note)
	case resolutionDisabled:
		return logf(w, "warning: %s toolchain %s, and toolchain.enabled is false — continuing with what is on PATH\n",
			req.Lang, r.lacking(req))
	case resolutionFailed:
		return logf(w, "warning: could not provision the %s toolchain (%s): %s — continuing with what is on PATH\n",
			req.Lang, r.lacking(req), r.note)
	case resolutionProvisioned:
		// The install happened once, so it is reported once.
		if r.note == "" || r.noted {
			return nil
		}
		r.noted = true
		return logf(w, "%s\n", r.note)
	}
	return nil
}

// resolveOne probes for one requirement's toolchain and provisions it if what
// is present does not satisfy it. It executes and returns what happened; the
// caller writes the line, because the line names a component and this is
// shared between every component that wants the same toolchain.
func resolveOne(ctx context.Context, root string, req Requirement, ov Overrides) (resolution, error) {
	p, ok := probes[req.Lang]
	if !ok {
		return resolution{}, nil
	}
	ambient, present := installed(ctx, p)
	r := resolution{kind: resolutionAmbient, bin: p.bin, ambient: ambient, present: present}

	good := satisfied(req, ambient, present)
	if req.Lang == runner.Rust {
		// Rust's verdict comes from rustup, asked in the component's own
		// directory — the only place the channel a cargo invocation there
		// selects can be established, and the only authority on a named
		// channel, which has no version to compare. See rustReady.
		//
		// The toolchain rustup names replaces the probed cargo version as what
		// this resolved to, because that version is the machine's default
		// channel and not necessarily the one this component runs under.
		//
		// An override selects a channel rustup's own file-based resolution
		// does not know about — it lives only in .lydite/config.yml — so the
		// question has to be asked with RUSTUP_TOOLCHAIN set, the same way
		// provisioning selects it below. Asking without it checks the
		// channel the directory would resolve to on its own, which can
		// already be ready and read the override as satisfied without ever
		// applying it.
		var env []string
		if req.Overridden {
			env = []string{"RUSTUP_TOOLCHAIN=" + req.Raw}
		}
		r.ambient, good, r.lack = rustReady(ctx, componentDir(root, req), env)
		// The ambient line names r.bin as what answered — rustup, not cargo,
		// now that r.ambient is the toolchain name rustup reports rather than
		// a cargo version.
		r.bin = "rustup"
	}

	if good {
		// Satisfied is not the same as nothing to do. Go still needs its
		// toolchain pinned so that installing an external tool cannot
		// silently switch away from the version just verified — see
		// pinAmbientGo. This costs no download and is why the branch
		// doesn't simply return nil.
		if req.Lang == runner.Go {
			if st := pinAmbientGo(req); st != nil {
				// The ambient version is recorded, not applied. It is the only
				// thing telling two ambient toolchains apart — this branch
				// contributes no directory and only GOTOOLCHAIN=local, which
				// names every ambient Go there has ever been — so a caller
				// caching a tool built under it has nothing else to key on.
				r.env = &Env{Vars: st.vars, Resolved: display(r.ambient)}
				// The pin is about the machine, not about any one component,
				// so it is said once alongside the per-component line.
				r.note = st.note
			}
			return r, nil
		}
		// Every other language contributes no directory and no variable here,
		// and still needs an environment carrying what it resolved to. The
		// toolchain is half of what measured a component's coverage — its
		// LLVM writes the line records in an lcov — and a baseline records
		// what measured it. Without this the ambient-satisfied path answers
		// with no version at all, so a machine that already has the right
		// toolchain records a different producer from one that provisioned
		// it, and the two never compare.
		if r.ambient != "" {
			r.env = &Env{Resolved: display(r.ambient)}
			// Being ready is not the same as being selected. An override
			// channel that is already installed still needs RUSTUP_TOOLCHAIN
			// carried into the environment a later cargo invocation uses —
			// without it, "the override channel has clippy and rustfmt" and
			// "the component actually runs under it" are two different
			// claims, and only rustReady's own verdict, just above, checked
			// the first.
			if req.Lang == runner.Rust && req.Overridden {
				r.env.Vars = []string{"RUSTUP_TOOLCHAIN=" + req.Raw}
			}
		}
		return r, nil
	}
	if ov.Disabled {
		r.kind = resolutionDisabled
		return r, nil
	}

	st, err := provision(ctx, req, r.ambient, r.present)
	if err != nil {
		r.kind, r.note = resolutionFailed, err.Error()
		return r, nil
	}
	if st == nil {
		r.kind = resolutionNone
		return r, nil
	}
	for _, dir := range st.pathDirs {
		ensureExecutable(dir)
	}
	r.kind, r.note = resolutionProvisioned, st.note
	// What was installed, not what was asked for. The declaration is a channel
	// or a floor — "stable", "1.26" — and recording it would describe every
	// release that ever satisfied it, so the same toolchain would carry one
	// identity on a runner that already had it and another on a runner that
	// installed it. Env.Resolved is a baseline's producer and half of a tool
	// cache's key, and both compare it verbatim.
	resolved, err := confirm(ctx, root, req, st)
	if err != nil {
		// The install succeeded, so the environment is real and keeping it is
		// not in question; only the identity went unestablished. The
		// declaration stands in for it, and the line says so rather than
		// letting an unconfirmed version read as a measured one.
		resolved = displayRaw(req)
		r.note += fmt.Sprintf("; warning: could not confirm %s's installed version (%v) — recording %q",
			req.Lang, err, resolved)
	}
	r.env = &Env{PathDirs: st.pathDirs, Vars: st.vars, Resolved: resolved}
	return r, nil
}

// confirm asks the toolchain a provisioning step has just installed what
// version it is, under the environment that step produced.
func confirm(ctx context.Context, root string, req Requirement, st *step) (string, error) {
	dir := componentDir(root, req)
	if req.Lang == runner.Rust {
		// rustup's answer, for the same reason the satisfied check takes it:
		// the channel a cargo invocation in this directory selects is the one
		// that will do the work, and a named channel has no version of its own
		// to report.
		active, _, lack := rustReady(ctx, dir, Compose(st.pathDirs, nil, st.vars))
		if active == "" {
			// The caller discards this string on a non-nil error and
			// substitutes its own fallback, so what matters here is the
			// error, not the value — returning active rather than a literal
			// keeps that true by construction instead of by convention.
			return active, errors.New(lack)
		}
		return display(active), nil
	}
	p, ok := probes[req.Lang]
	if !ok {
		return "", fmt.Errorf("lydite has no %s probe", req.Lang)
	}
	version, err := probeUnder(ctx, p, st, dir)
	if err != nil {
		return "", err
	}
	// Rendered the way the ambient path renders its own answer, because the
	// two are compared as strings by everything that reads them.
	return display(version), nil
}

// componentDir is the component's directory on disk, which is where a
// toolchain question about it has to be asked.
func componentDir(root string, req Requirement) string {
	return filepath.Join(root, filepath.FromSlash(req.Unit.Dir))
}

// provision dispatches to the per-language provisioner. Each one differs in
// kind, not just in URL: Go delegates to its own GOTOOLCHAIN mechanism, Rust
// delegates to rustup, and only Node is downloaded and unpacked by lydite.
func provision(ctx context.Context, req Requirement, ambient string, present bool) (*step, error) {
	switch req.Lang {
	case runner.Go:
		return provisionGo(ctx, req, ambient, present)
	case runner.Rust:
		// ambient is rustReady's active channel for Rust — resolveOne set
		// r.ambient to it before deciding provisioning was needed — the
		// channel this directory already resolves to, which an unpinned
		// component falls back to installing.
		return provisionRust(ctx, req, ambient)
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

// lacking renders why the ambient toolchain was not good enough, preferring
// the language's own answer over the generic version comparison when it has
// one.
func (r *resolution) lacking(req Requirement) string {
	if r.lack != "" {
		return r.lack
	}
	return shortfall(req, r.ambient, r.present)
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
