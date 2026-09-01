// Package cargotool installs the cargo subcommands lydite runs, at the exact
// version lydite pins.
//
// lydite pins every tool it invokes and installs it into a version-keyed cache
// rather than trusting whatever is on the machine. A security scanner that
// nothing ever ages out is one that goes stale while still reporting a pass,
// and a scanner whose version varies by runner is one whose verdict does too.
// See docs/adr/0006-tool-pins-as-dependabot-manifests.md.
//
// Every version comes from a real Cargo.toml that Dependabot watches, never
// from a Go constant: a pin nothing can bump is a pin nobody will.
package cargotool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"lydite/lydite/internal/download"
	"lydite/lydite/internal/executil"
)

// Asset is a prebuilt release archive for this machine.
type Asset struct {
	// URL is the .tar.gz.
	URL string
	// ChecksumURL is a file whose first field is the archive's SHA-256. The
	// digest is read out of band and never derived from the download, because
	// lydite is about to put this binary on PATH and execute it.
	ChecksumURL string
}

// Tool is one pinned cargo subcommand.
type Tool struct {
	// Name is the crate, which is also the binary: cargo finds `cargo-nextest`
	// on PATH and offers it as `cargo nextest`.
	Name string
	// Version is exact, read from the pin manifest.
	Version string
	// Prebuilt locates a released binary for the running machine, or reports
	// false when the publisher ships none for it.
	//
	// Building cargo-nextest from source is around seven minutes and the
	// prebuilt is three seconds, which is not a speed-up but the difference
	// between a first run someone waits through and one they abandon. A
	// publisher that ships no archive for a platform is not an error: the
	// source build below still works, just slowly.
	Prebuilt func(version string) (Asset, bool)
}

// Install puts this exact version in the cache, and does nothing when it is
// already there.
//
// The prebuilt archive is tried first and `cargo install` is the fallback, so
// a platform with no release, a proxy that blocks the download, or a checksum
// that does not match all still end up with the pinned version — slowly, and
// having said so, rather than not at all. progress is where the source build's
// output goes; the download is quiet because there is nothing to watch.
func (t Tool) Install(ctx context.Context, progress io.Writer) error {
	if t.Installed() {
		return nil
	}
	// One install of a given tool at a time. `lydite test` prepares its
	// components concurrently, so two Rust components ask for the same pinned
	// nextest at once — and while the prebuilt path stages into a sibling
	// directory and renames, the source fallback runs `cargo install --root`
	// twice against one root, where the two contend over its .crates.toml.
	// That fallback is the path a blocked download or a mismatched checksum
	// already selected, which is the worst place to add a second failure.
	unlock := lockTool(t.Name + "@" + t.Version)
	defer unlock()
	// Re-checked under the lock: the install this one waited for is very
	// likely the one it was about to do.
	if t.Installed() {
		return nil
	}
	root, err := t.Root()
	if err != nil {
		return err
	}
	if t.Prebuilt != nil {
		if err := t.installPrebuilt(ctx, root); err == nil {
			return nil
		} else if !errors.Is(err, errNoPrebuilt) {
			// Named, never swallowed: falling back silently would make a
			// seven-minute build look like the normal cost of a first run.
			fmt.Fprintf(os.Stderr, "lydite: %s prebuilt unavailable (%v); building from source\n", t.Name, err)
		}
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	argv, err := t.InstallArgv()
	if err != nil {
		return err
	}
	if res := executil.RunOutput(ctx, "", nil, progress, "cargo", argv...); !res.Ok() {
		return fmt.Errorf("cargo install %s@%s: %w", t.Name, t.Version, res.Err)
	}
	return nil
}

// installLocks serialises installs of one tool version within this process.
//
// In-process only, and that is the whole of what it claims: two lydite
// processes installing the same tool still race, which the staged-and-renamed
// prebuilt path already tolerates and the source build has always been able to
// hit. What it closes is the case this process creates for itself by preparing
// components concurrently.
var installLocks sync.Map

func lockTool(key string) func() {
	v, _ := installLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// errNoPrebuilt says the publisher ships nothing for this machine, which is a
// reason to build rather than something to report as a failure.
var errNoPrebuilt = errors.New("no prebuilt release for this platform")

// installPrebuilt downloads the release archive and stages it into place.
//
// Staged and renamed, never written in place: an interrupted download would
// otherwise leave a directory the next run reads as a finished install, which
// is the same guard internal/toolchain applies to a language toolchain.
func (t Tool) installPrebuilt(ctx context.Context, root string) error {
	asset, ok := t.Prebuilt(t.Version)
	if !ok {
		return errNoPrebuilt
	}
	sums, err := download.Fetch(ctx, asset.ChecksumURL)
	if err != nil {
		return err
	}
	sum := strings.Fields(string(sums))
	if len(sum) == 0 {
		return fmt.Errorf("%s: no checksum at %s", t.Name, asset.ChecksumURL)
	}
	data, err := download.Verified(ctx, asset.URL, sum[0])
	if err != nil {
		return err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	// A sibling of the destination, because os.Rename cannot cross
	// filesystems and os.TempDir is routinely a different mount.
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(root)+".tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	// Zero components stripped: a release archive of one binary has no
	// wrapping directory, and stripping would discard the only entry.
	if err := download.ExtractTarGz(data, filepath.Join(staging, "bin"), 0); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", t.Name)); err != nil {
		return fmt.Errorf("%s: the release archive holds no %s", t.Name, t.Name)
	}
	if err := os.Rename(staging, root); err != nil {
		// Another lydite finished first, which is a success and not a race to
		// report.
		if t.Installed() {
			return nil
		}
		return err
	}
	return nil
}

// Root is where this exact version is installed, keyed by version so two
// lydite releases pinning different ones do not overwrite each other and a
// downgrade does not silently keep the newer binary.
func (t Tool) Root() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "lydite", t.Name+"-"+t.Version), nil
}

// BinDir is the directory the installed binary lives in — what to prepend to
// PATH for a tool that is invoked as a cargo subcommand rather than directly.
func (t Tool) BinDir() (string, error) {
	root, err := t.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin"), nil
}

// Binary is the installed executable, which may not exist yet.
func (t Tool) Binary() (string, error) {
	dir, err := t.BinDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, t.Name), nil
}

// Installed reports whether this exact version is already in the cache.
func (t Tool) Installed() bool {
	bin, err := t.Binary()
	if err != nil {
		return false
	}
	_, err = os.Stat(bin)
	return err == nil
}

// InstallArgv is the cargo invocation that puts this version in the cache.
//
// --locked, so the crate's own committed lockfile decides its dependencies:
// without it cargo re-resolves and the tool lydite installs today is built
// from a different graph than the one its authors tested, which is a pin in
// name only.
func (t Tool) InstallArgv() ([]string, error) {
	root, err := t.Root()
	if err != nil {
		return nil, err
	}
	return []string{"install", "--locked", "--root", root, "--version", t.Version, t.Name}, nil
}

// PinnedVersion extracts `crate = "=x.y.z"` from a pin manifest's
// [dependencies] table.
//
// Two lines of TOML do not justify a TOML dependency, but they do justify
// being strict on two counts. An unparseable manifest must fail loudly rather
// than yield "", which would become `cargo install --version ”` — a confusing
// failure far from its cause, or worse, an install of whatever is latest. And
// the scan must be scoped to [dependencies]: the manifest also carries
// `version = "0.0.0"` and `name = "..."` in its [package] table, so a
// section-blind parser answers a lookup with unrelated package metadata.
func PinnedVersion(manifest []byte, crate string) (string, error) {
	inDeps := false
	for line := range strings.Lines(string(manifest)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inDeps = line == "[dependencies]"
			continue
		}
		if !inDeps {
			continue
		}
		name, rest, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != crate {
			continue
		}
		v := strings.TrimSpace(rest)
		// Strip a trailing inline comment before unquoting. Without this,
		// `cargo-audit = "=0.22.2" # note` survives as `0.22.2" # note` and is
		// handed to `cargo install --version` — a corrupt value where the doc
		// above promises a loud failure. This repo already appends
		// `# nosemgrep:` comments to config lines elsewhere, so it is not a
		// hypothetical shape.
		if i := strings.Index(v, "#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		unquoted, ok := strings.CutPrefix(v, `"`)
		if !ok {
			return "", fmt.Errorf("%s: version for %s is not a quoted string", crate, crate)
		}
		v, ok = strings.CutSuffix(unquoted, `"`)
		if !ok {
			return "", fmt.Errorf("%s: version for %s has no closing quote", crate, crate)
		}
		// The `=` prefix is cargo's exact-version requirement operator; lydite
		// wants the bare version to hand to `cargo install --version`.
		v = strings.TrimPrefix(v, "=")
		if v == "" {
			return "", fmt.Errorf("%s has an empty version", crate)
		}
		return v, nil
	}
	return "", fmt.Errorf("no [dependencies] entry for %s", crate)
}

// MustPinnedVersion panics on a manifest that cannot be read, which is a build
// error rather than a runtime one: the manifest is embedded, so a bad one
// cannot appear on a user's machine without appearing in CI first.
func MustPinnedVersion(manifest []byte, crate string) string {
	v, err := PinnedVersion(manifest, crate)
	if err != nil {
		panic(err)
	}
	return v
}
