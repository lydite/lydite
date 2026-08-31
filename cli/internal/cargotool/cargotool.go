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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tool is one pinned cargo subcommand.
type Tool struct {
	// Name is the crate, which is also the binary: cargo finds `cargo-nextest`
	// on PATH and offers it as `cargo nextest`.
	Name string
	// Version is exact, read from the pin manifest.
	Version string
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
