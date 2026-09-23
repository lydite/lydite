package toolchain

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- only for a registry version publishing no SHA-512 integrity; see verifyDist
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"lydite/lydite/internal/download"
	"lydite/lydite/internal/executil"
)

// goBootstrapMin is the first Go release that can fetch another toolchain for
// itself (the GOTOOLCHAIN mechanism, Go 1.21). At or above it, lydite never
// downloads Go: it names the version it wants and lets the go command fetch
// it through the module proxy, verified against Go's checksum database. Below
// it — or with no Go at all — there is nothing to delegate to and lydite
// downloads a tarball itself.
const goBootstrapMin = "v1.21"

// provisionGo makes the declared Go toolchain available.
//
// The common case does no downloading at all. Any Go 1.21+ on the machine can
// fetch a newer toolchain on demand, so lydite just sets GOTOOLCHAIN to the
// version the repo declares and lets Go do it — the official mechanism, and
// better provenance than anything lydite could hand-roll.
//
// Setting it explicitly also closes a real, documented gap. GOTOOLCHAIN's
// default (`auto`) consults go.mod only when the go command runs inside that
// module; `go install <external tool>@<version>`, which internal/golang runs
// outside any module to fetch gosec and govulncheck, does not consult it. So
// those tools got built with whatever Go the runner shipped, and a
// govulncheck built by an older Go rejects newer source outright — the exact
// failure agentic/references/ci.md records against wardnet's CI, worked around there by
// pinning `go-version` in every workflow. An explicit GOTOOLCHAIN fixes it at
// the source instead of in each consumer's YAML.
//
// [lydite:exclude_from_coverage][the proving ground provisions a bare checkout end to end; a unit
// test here would resolve the machine's own Go rather than lydite's code]
func provisionGo(ctx context.Context, req Requirement, ambient string, present bool) (*step, error) {
	if req.Unpinned() {
		if present {
			return nil, nil
		}
		return nil, fmt.Errorf("no Go toolchain on PATH and no version declared in any go.mod to install one by")
	}
	want := toolchainName(req.Version)

	if present && !olderThan(ambient, goBootstrapMin) {
		return &step{
			vars: []string{"GOTOOLCHAIN=" + want},
			note: fmt.Sprintf("go: using %s via GOTOOLCHAIN (declared in %s; ambient go is %s)",
				want, req.Source, display(ambient)),
		}, nil
	}

	dir, err := cacheRoot("go-" + strings.TrimPrefix(req.Version, "v"))
	if err != nil {
		return nil, err
	}
	if err := installOnce(dir, func(staging string) error {
		return downloadGo(ctx, want, staging)
	}); err != nil {
		return nil, err
	}
	return &step{
		pathDirs: []string{filepath.Join(dir, "bin")},
		// The downloaded toolchain is exactly what was asked for and is now
		// first on PATH, so "local" both selects it and stops the go command
		// switching away from it. See pinAmbientGo for why not switching
		// matters as much as switching to the right one.
		vars: []string{"GOTOOLCHAIN=local"},
		note: fmt.Sprintf("go: installed %s (declared in %s; %s)", want, req.Source, absentOrOld(ambient, present)),
	}, nil
}

// pinAmbientGo pins GOTOOLCHAIN when the ambient Go already satisfies the
// declared version — the case where lydite installs nothing at all.
//
// Doing nothing here would leave the headline bug this package claims to fix
// still present in its most common path. GOTOOLCHAIN defaults to `auto`, and
// `auto` does not only *upgrade*: running `go install <tool>@<version>`
// outside a module, which internal/golang does to fetch gosec and
// govulncheck, makes the go command consult that TOOL's own go.mod and
// silently switch to whatever minimum it declares. golang.org/x/vuln@v1.6.0
// declares `go >= 1.25.0`, so an `auto` runner with Go 1.26 installed builds
// govulncheck with go1.25 — and a govulncheck built by an older Go rejects
// newer source outright. That is the exact failure agentic/references/ci.md records against
// wardnet's CI, and it reproduces on a runner whose ambient toolchain is
// perfectly correct.
//
// "local" is the right pin, not the declared version. The declared version is
// a *minimum*: a `go 1.26` directive resolves to go1.26.0, and pinning that
// on a runner with 1.26.6 would downgrade a toolchain that was already fine —
// which, when the newer patch is the one carrying a security fix, is exactly
// backwards. "local" says "use the toolchain we just verified satisfies the
// declaration, and never switch away from it".
//
// Only Go needs this. cargo and node have no equivalent of a tool's own
// manifest silently re-selecting the runtime mid-invocation.
func pinAmbientGo(req Requirement) *step {
	if req.Unpinned() {
		// Nothing was declared, so nothing was verified, so there is no
		// ground for overriding whatever the environment already chose.
		return nil
	}
	return &step{
		vars: []string{"GOTOOLCHAIN=local"},
		note: "go: pinned GOTOOLCHAIN=local so installing an external tool cannot silently downgrade the toolchain",
	}
}

// toolchainName renders a canonical version as a Go toolchain name.
// Go toolchains are always named to the patch level ("go1.26.0", never
// "go1.26"), so a go.mod that says only `go 1.26` is resolved to that
// minor's initial release — the oldest toolchain that satisfies it, which is
// the right reading of a minimum.
func toolchainName(version string) string {
	v := strings.TrimPrefix(version, "v")
	if strings.Count(v, ".") < 2 {
		v += strings.Repeat(".0", 2-strings.Count(v, "."))
	}
	return "go" + v
}

// goRelease is the subset of https://go.dev/dl/?mode=json lydite reads.
type goRelease struct {
	Version string `json:"version"`
	Files   []struct {
		Filename string `json:"filename"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Kind     string `json:"kind"`
		SHA256   string `json:"sha256"`
	} `json:"files"`
}

// downloadGo fetches a Go toolchain tarball into staging, taking the expected
// SHA-256 from Go's own release index rather than from anything alongside the
// download.
//
// [lydite:exclude_from_coverage][reached only on a machine carrying no Go at all or one older than
// 1.21, which no runner lydite tests on is; exercising it means downloading
// a toolchain, and a test that did would measure nodejs.org's availability]
func downloadGo(ctx context.Context, version, staging string) error {
	index, err := download.Fetch(ctx, "https://go.dev/dl/?mode=json&include=all")
	if err != nil {
		return err
	}
	var releases []goRelease
	if err := json.Unmarshal(index, &releases); err != nil {
		return err
	}
	for _, rel := range releases {
		if rel.Version != version {
			continue
		}
		for _, f := range rel.Files {
			if f.OS != runtime.GOOS || f.Arch != runtime.GOARCH || f.Kind != "archive" {
				continue
			}
			data, err := download.Verified(ctx, "https://go.dev/dl/"+f.Filename, f.SHA256)
			if err != nil {
				return err
			}
			return download.ExtractTarGz(data, staging, 1)
		}
		return fmt.Errorf("go toolchain %s has no %s/%s archive", version, runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Errorf("go toolchain %s is not in the release index", version)
}

// provisionRust makes the declared Rust toolchain available.
//
// Rust is the one ecosystem with an official, universally-used version
// manager that already reads the same file lydite does: given a
// rust-toolchain.toml, rustup installs and selects that channel by itself.
// internal/rust's package doc already leans on exactly that ("clippy/fmt's
// own toolchain version is the target repo's responsibility via its own
// rust-toolchain.toml — lydite doesn't second-guess that"), so lydite
// provisions *rustup* and lets rustup provision Rust, rather than duplicating
// its channel resolution.
//
// What this adds over rustup's own laziness is materialising the toolchain up
// front, with the components the checks actually need. rustup would otherwise
// install it on first use, in the middle of `cargo clippy`, and a missing
// clippy component surfaces as a confusing check failure rather than as a
// setup step.
//
// There is no "already good enough" short-circuit here, and there must not be
// one: rustReady has already established that the channel this component
// resolves to is missing or incomplete. A version comparison at this point
// would compare the machine's default channel against the declared one and
// return a no-op for exactly the case the caller sent it here to fix.
//
// active is the channel rustReady found this component's directory already
// resolving to, before any install. It is what an unpinned component falls
// back to, and it must be: with no rust-toolchain.toml and no override,
// installing a channel selects nothing — only the file rustup already reads,
// or RUSTUP_TOOLCHAIN, changes what a later cargo invocation picks. A
// hardcoded "stable" would install clippy and rustfmt onto a toolchain
// nothing runs under, leaving the component to keep failing on its original,
// incomplete default.
func provisionRust(ctx context.Context, req Requirement, active string) (*step, error) {
	if !executil.Available("rustup") {
		return nil, fmt.Errorf(
			"rustup is not installed; install it (https://rustup.rs) so lydite can provision the %s toolchain. A cargo on PATH is not enough: rustup is what resolves a component's channel and what installs clippy and rustfmt for it",
			displayRaw(req))
	}
	// rustup needs a concrete channel. The repo's own word for it is the
	// right one to pass — "stable" and "1.96" are both valid channels, and
	// rustup understands them where lydite's version comparison cannot. An
	// unpinned component has no such word: the channel it needs is the one
	// rustup already resolves this directory to, not an assumed default.
	channel := req.Raw
	if channel == "" {
		channel = active
	}
	if channel == "" {
		channel = "stable"
	}
	// Idempotent: a channel already installed makes this a no-op that exits
	// zero, which is why it is safe to run on every scan.
	r := executil.Run(ctx, "", "rustup", "toolchain", "install", channel,
		"--profile", "minimal", "--component", "clippy", "--component", "rustfmt")
	if !r.Ok() {
		return nil, fmt.Errorf("rustup toolchain install %s: %w", channel, r.Err)
	}

	note := fmt.Sprintf("rust: installed toolchain %s with clippy and rustfmt via rustup", channel)
	// Named only when a manifest actually named it. A component declaring no
	// channel is installed at "stable", and "declared in " with nothing after
	// it reads as a manifest lydite failed to name rather than as one that was
	// never written.
	if req.Source != "" {
		note += " (declared in " + req.Source + ")"
	}
	st := &step{note: note}
	// Installing is not selecting. rustup picks a toolchain by reading
	// rust-toolchain.toml from the directory cargo runs in, which covers the
	// normal case for free — internal/rust and internal/coverage both run
	// cargo inside the crate directory, so the file lydite read is the file
	// rustup reads.
	//
	// An override has no such file. The version exists only in .lydite/config.yml,
	// rustup cannot see it, and without being told it would install the
	// requested channel and then go on running the old default — reporting
	// success for a toolchain nothing actually uses. RUSTUP_TOOLCHAIN is the
	// explicit selection, and it is set *only* here: applying it whenever a
	// channel was read from a manifest would override rustup's own per-crate
	// selection, which is exactly what internal/rust's "lydite doesn't
	// second-guess that" stance says not to do, and would break a monorepo
	// whose crates pin different channels.
	if req.Overridden {
		st.vars = []string{"RUSTUP_TOOLCHAIN=" + channel}
		st.note += " and selected it via RUSTUP_TOOLCHAIN (the version came from config, so no rust-toolchain.toml names it)"
	}
	return st, nil
}

// provisionNode makes the declared Node runtime available.
//
// Node has no equivalent of GOTOOLCHAIN or rustup that can be assumed present
// — nvm/fnm/volta are all optional and mutually exclusive — so this is the
// one ecosystem lydite downloads and unpacks itself, from nodejs.org, with
// the digest read from that release's SHASUMS256.txt rather than from the
// archive.
//
// [lydite:exclude_from_coverage][the proving ground provisions a bare checkout end to end; a unit
// test here would resolve the machine's own Node rather than lydite's code]
func provisionNode(ctx context.Context, req Requirement, ambient string, present bool) (*step, error) {
	if req.Unpinned() {
		if present {
			return nil, nil
		}
		return nil, fmt.Errorf("no Node on PATH, and neither engines.node nor .nvmrc declares a version to install")
	}
	if present && !olderThan(ambient, req.Version) {
		return nil, nil
	}
	version := nodeReleaseName(req.Version)
	dir, err := cacheRoot("node-" + strings.TrimPrefix(version, "v"))
	if err != nil {
		return nil, err
	}
	if err := installOnce(dir, func(staging string) error {
		return downloadNode(ctx, version, staging)
	}); err != nil {
		return nil, err
	}
	return &step{
		pathDirs: []string{filepath.Join(dir, "bin")},
		note: fmt.Sprintf("typescript: installed Node %s (declared in %s; %s)",
			version, req.Source, absentOrOld(ambient, present)),
	}, nil
}

// nodeReleaseName renders a canonical version as a Node release directory
// name. Node releases are always MAJOR.MINOR.PATCH, so a floor of ">=20"
// resolves to that major's first release.
func nodeReleaseName(version string) string {
	v := strings.TrimPrefix(version, "v")
	if n := strings.Count(v, "."); n < 2 {
		v += strings.Repeat(".0", 2-n)
	}
	return "v" + v
}

// downloadNode fetches a Node tarball into staging.
//
// The .tar.gz is chosen over the .tar.xz that nodejs.org also publishes
// purely because Go's standard library can decompress gzip and cannot
// decompress xz; taking the xz would mean either a new dependency or shelling
// out to a tar binary that may not exist on a minimal image.
//
// [lydite:exclude_from_coverage][
// reached only when the declared engines.node is not already satisfied,
// which no runner lydite tests on leaves unsatisfied; exercising it means
// downloading a toolchain, and a test that did would measure nodejs.org]
func downloadNode(ctx context.Context, version, staging string) error {
	base := "https://nodejs.org/dist/" + version
	name := fmt.Sprintf("node-%s-%s-%s.tar.gz", version, nodeOS(), nodeArch())

	sums, err := download.Fetch(ctx, base+"/SHASUMS256.txt")
	if err != nil {
		return err
	}
	want := ""
	for line := range strings.SplitSeq(string(sums), "\n") {
		sum, file, found := strings.Cut(strings.TrimSpace(line), "  ")
		if found && file == name {
			want = sum
			break
		}
	}
	if want == "" {
		return fmt.Errorf("node %s publishes no %s", version, name)
	}
	data, err := download.Verified(ctx, base+"/"+name, want)
	if err != nil {
		return err
	}
	return download.ExtractTarGz(data, staging, 1)
}

// nodeOS and nodeArch translate Go's platform names into the ones nodejs.org
// uses in its filenames. Only the pair lydite itself is built for matters —
// linux/darwin × amd64/arm64, per .goreleaser.yml.
func nodeOS() string { return runtime.GOOS }

func nodeArch() string {
	if runtime.GOARCH == "amd64" {
		return "x64"
	}
	return runtime.GOARCH
}

// npmRegistry is where a package manager's pinned release is fetched from,
// and the only host its tarball may be fetched from.
const npmRegistry = "https://registry.npmjs.org/"

// provisionPackageManager makes the pnpm or yarn release a workspace pins in
// `packageManager` available as a command.
//
// Downloaded from the npm registry into the version-keyed cache, the way Node
// is, rather than enabled through Corepack: Corepack's shims are written into
// Node's own install directory, which on a runner is not lydite's to modify,
// and what it fetches is the same registry tarball this fetches.
//
// satisfied has already established that the ambient release is absent or is
// not the pinned one, so there is no "already good enough" check here.
func provisionPackageManager(ctx context.Context, req Requirement, ambient string, present bool) (*step, error) {
	version := req.Raw
	dir, err := cacheRoot(req.Manager + "-" + version)
	if err != nil {
		return nil, err
	}
	if err := installOnce(dir, func(staging string) error {
		return downloadPackageManager(ctx, req.Manager, version, staging)
	}); err != nil {
		return nil, fmt.Errorf("%s %s (pinned in %s) could not be installed from %s: %w",
			req.Manager, version, req.Source, npmRegistry, err)
	}
	note := fmt.Sprintf("typescript: installed %s %s (pinned in %s; ", req.Manager, version, req.Source)
	if present {
		note += "ambient " + display(ambient) + " is not the pinned release)"
	} else {
		note += "none was on PATH)"
	}
	return &step{pathDirs: []string{filepath.Join(dir, "bin")}, note: note}, nil
}

// registryPackage is the npm package a manager's release is published as.
// Yarn 2 and later is not the `yarn` package, which stops at 1.x: it is
// @yarnpkg/cli-dist, which is where Corepack fetches it from too.
func registryPackage(manager, version string) string {
	if manager == "yarn" && !olderThan("v"+version, "v2") {
		return "@yarnpkg/cli-dist"
	}
	return manager
}

// registryVersion is the subset of the npm registry's document for one
// published version that lydite reads.
type registryVersion struct {
	Dist registryDist `json:"dist"`
}

// registryDist is where a published version's tarball is and what it hashes
// to.
type registryDist struct {
	Tarball string `json:"tarball"`
	// Integrity is a Subresource Integrity string ("sha512-<base64>"), which
	// may carry more than one space-separated digest.
	Integrity string `json:"integrity"`
	// Shasum is the SHA-1 hex digest every published version carries,
	// including those older than the registry's integrity field.
	Shasum string `json:"shasum"`
}

// downloadPackageManager fetches a package manager's release tarball into
// staging, taking its digest from the registry's own document for that
// version rather than from anything alongside the tarball.
//
// [lydite:exclude_from_coverage][reached only when the pinned release is not
// already on PATH or in the cache; exercising it means downloading a package
// manager, and a test that did would measure registry.npmjs.org]
func downloadPackageManager(ctx context.Context, manager, version, staging string) error {
	pkg := registryPackage(manager, version)
	doc, err := download.Fetch(ctx, npmRegistry+pkg+"/"+version)
	if err != nil {
		return err
	}
	var meta registryVersion
	if err := json.Unmarshal(doc, &meta); err != nil {
		return fmt.Errorf("%s@%s: %w", pkg, version, err)
	}
	if !strings.HasPrefix(meta.Dist.Tarball, npmRegistry) {
		return fmt.Errorf("%s@%s names its tarball at %q, outside %s", pkg, version, meta.Dist.Tarball, npmRegistry)
	}
	data, err := download.Fetch(ctx, meta.Dist.Tarball)
	if err != nil {
		return err
	}
	if err := verifyDist(data, meta.Dist); err != nil {
		return err
	}
	return unpackManager(data, staging, manager)
}

// verifyDist checks a tarball against the digest the registry publishes for
// it, before a byte of it is unpacked.
//
// SHA-512 from the integrity field when there is one, which is the strength
// Node's own SHASUMS256.txt check gives; the SHA-1 shasum only when the
// registry publishes nothing stronger. A version publishing neither is
// refused rather than trusted, for the reason download.Verified gives: this
// archive is about to be put on PATH and executed.
func verifyDist(data []byte, dist registryDist) error {
	for field := range strings.FieldsSeq(dist.Integrity) {
		encoded, ok := strings.CutPrefix(field, "sha512-")
		if !ok {
			continue
		}
		want, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("%s publishes an unreadable integrity %q: %w", dist.Tarball, field, err)
		}
		got := sha512.Sum512(data)
		if !bytes.Equal(got[:], want) {
			return fmt.Errorf("checksum mismatch for %s: got sha512-%s, want %s",
				dist.Tarball, base64.StdEncoding.EncodeToString(got[:]), field)
		}
		return nil
	}
	if dist.Shasum == "" {
		return fmt.Errorf("%s publishes no digest to verify it against", dist.Tarball)
	}
	sum := sha1.Sum(data) // #nosec G401 -- the registry's own digest for a version predating its integrity field; SHA-512 is used whenever one is published
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, dist.Shasum) {
		return fmt.Errorf("checksum mismatch for %s: got sha1 %s, want %s", dist.Tarball, got, dist.Shasum)
	}
	return nil
}

// unpackManager lays a verified package manager tarball out in staging: the
// package itself under package/, and under bin/ an executable named for the
// command that runs its entry point with node.
//
// A wrapper rather than the entry point itself on PATH. The entry is a
// `#!/usr/bin/env node` script whose name is not the command's ("pnpm.cjs",
// "yarn.js"), and running it through node explicitly resolves node the way
// every other command in the component's environment does — from the PATH
// that environment composes, where a provisioned Node comes first.
//
// The wrapper finds the package relative to itself, because installOnce
// unpacks into a staging directory and renames it into place afterwards.
func unpackManager(data []byte, staging, command string) error {
	pkgDir := filepath.Join(staging, "package")
	// Every registry tarball wraps its content in one top-level directory —
	// "package/" for most, "yarn-v1.22.19/" for yarn's own.
	if err := download.ExtractTarGz(data, pkgDir, 1); err != nil {
		return err
	}
	entry, err := binEntry(pkgDir, command)
	if err != nil {
		return err
	}
	binDir := filepath.Join(staging, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return err
	}
	// Parameter expansion rather than dirname, so the wrapper needs nothing
	// from PATH but the node it exists to run.
	wrapper := "#!/bin/sh\n" +
		"case \"$0\" in */*) here=\"${0%/*}\" ;; *) here=. ;; esac\n" +
		"exec node \"$here\"/" + shellQuote("../package/"+entry) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(binDir, command), []byte(wrapper), 0o755) // #nosec G306 -- an executable wrapper is the point
}

// binEntry is the file a package's `bin` field runs as command, relative to
// the package directory.
//
// npm's own two spellings are both read: a bare string is the package's one
// command, and an object maps each command it installs to its file. A path
// escaping the package, or naming no file in it, is refused — this is what the
// wrapper hands to node.
func binEntry(pkgDir, command string) (string, error) {
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json")) // #nosec G304 -- pkgDir is a staging directory this package just unpacked a verified archive into
	if err != nil {
		return "", err
	}
	var manifest struct {
		Bin json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("package.json: %w", err)
	}
	var entry string
	if json.Unmarshal(manifest.Bin, &entry) != nil {
		var commands map[string]string
		if json.Unmarshal(manifest.Bin, &commands) != nil {
			return "", fmt.Errorf("package.json declares no bin for %s", command)
		}
		entry = commands[command]
	}
	if entry == "" {
		return "", fmt.Errorf("package.json declares no bin for %s", command)
	}
	entry = path.Clean(entry)
	if !filepath.IsLocal(filepath.FromSlash(entry)) {
		return "", fmt.Errorf("package.json's bin for %s is %q, outside the package", command, entry)
	}
	if info, err := os.Stat(filepath.Join(pkgDir, filepath.FromSlash(entry))); err != nil || info.IsDir() {
		return "", fmt.Errorf("package.json's bin for %s is %q, which the package does not contain", command, entry)
	}
	return entry, nil
}

// shellQuote renders s as one single-quoted shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// step is one ecosystem's contribution to the resolved environment.
type step struct {
	pathDirs []string
	vars     []string
	note     string
}

// display renders a probed version for a message, naming the unidentifiable
// case rather than printing an empty string.
func display(version string) string {
	if version == "" {
		return "an unidentified version"
	}
	return strings.TrimPrefix(version, "v")
}

// displayRaw prefers what the manifest literally said over the canonical
// form, so a "stable" channel is reported as "stable".
func displayRaw(req Requirement) string {
	if req.Raw != "" {
		return req.Raw
	}
	return display(req.Version)
}

// absentOrOld describes why provisioning happened, which is the part a reader
// of the log actually wants: whether their runner had nothing, or had
// something too old.
func absentOrOld(ambient string, present bool) string {
	if !present {
		return "none was on PATH"
	}
	return "ambient " + display(ambient) + " is older"
}

// ensureExecutable is a defensive fix-up after extraction: archive modes are
// preserved by extractTarGz, but a tarball produced with a restrictive umask
// can still land a bin/ entry without the execute bit, which then fails at
// use with a bare "permission denied".
//
// [lydite:exclude_from_coverage][
// it runs against a toolchain the step above it has just unpacked, so
// the proving ground reaches it and a unit test here would chmod a fixture]
func ensureExecutable(binDir string) {
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			_ = os.Chmod(filepath.Join(binDir, e.Name()), info.Mode().Perm()|0o111) // #nosec G302 -- restoring the execute bit on a toolchain binary is the point
		}
	}
}
