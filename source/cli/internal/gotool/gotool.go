// Package gotool installs the Go programs lydite runs, into a version-keyed
// cache directory rather than trusting whatever is already on the machine.
//
// It exists for the reason internal/cargotool does: two packages need the same
// install, and the rule has to exist once. internal/golang fetches the
// scanners, internal/runner fetches the test-runner wrapper, and a second copy
// of "where does a pinned binary go and when is it stale" is a second chance
// to key a cache directory wrongly — where the failure is a stale tool
// reporting a pass, which is the worst outcome available to a security tool.
//
// Nothing here decides a version. The caller states one, and every version
// lydite states lives in a manifest Dependabot can see (ADR 0006).
package gotool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/executil"
)

// BinDir is where the named version of a tool is installed.
//
// Keyed by name and version, plus whatever the caller adds. A bump therefore
// gets a fresh directory instead of silently reusing what is already there,
// which is the property that makes a pin mean anything.
func BinDir(name, version, key string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := "gobin-" + name + "-" + version
	if key != "" {
		dir += "-" + key
	}
	return filepath.Join(cache, "lydite", dir), nil
}

// Ensure installs pkg unless the cache already holds that exact version, and
// returns the path to the binary.
//
// env is the environment the install runs under, and is deliberately the
// caller's INSTALL environment rather than its check environment: `go install`
// reads GOPROXY and GOSUMDB, so a scanned repository's own declared
// environment reaching this would choose where lydite's tools come from — and
// the cache key names the version, not where it came from, so one substituted
// build outlives the run and, on a shared ~/.cache/lydite, reaches other
// repositories.
//
// key distinguishes installs that must not share a directory beyond the
// version. internal/golang passes the resolved Go toolchain, because a tool
// that analyses source is built by the toolchain it will be asked about and a
// tool built by an older Go rejects newer source outright. A wrapper that only
// reads another command's output has no such coupling and passes nothing.
func Ensure(ctx context.Context, env []string, name, version, pkg, key string) (string, error) {
	binDir, err := BinDir(name, version, key)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(binDir, name)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return "", err
	}
	r := executil.RunEnv(ctx, "", append(append([]string{}, env...), "GOBIN="+binDir), "go", "install", pkg)
	if !r.Ok() {
		// The command's own output, not just its exit status: `exit status 1`
		// is what a failing row would otherwise carry into --json, which is
		// the document the pull-request comment renders.
		return "", fmt.Errorf("installing %s: %w\n%s", pkg, r.Err, strings.TrimSpace(r.Output))
	}
	return bin, nil
}
