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
	return EnsureWith(ctx, env, name, version, pkg, key, nil)
}

// A Require is a module whose version the install must use, whatever the tool
// itself asks for.
type Require struct {
	Module  string
	Version string
}

// EnsureWith is Ensure for a tool built against dependency versions lydite
// chooses rather than the ones the tool's own go.mod resolves to.
//
// `go install pkg@version` builds with exactly the versions that module
// declares, so a tool that analyses source is only as current as the
// golang.org/x/tools it was released with. That library reads the compiler's
// export data, and a Go release that writes a newer format than the pinned
// reader knows makes every import fail with "export data version N is greater
// than maximum supported" — a tool that then scans nothing. Raising the
// dependency has to happen at build time, in a module of its own, because
// `go install pkg@version` refuses to take requirements from anywhere else.
//
// The requirements are part of the install's identity: they join the cache
// directory's name, so changing one builds again instead of reusing a binary
// made against the old version.
func EnsureWith(ctx context.Context, env []string, name, version, pkg, key string, require []Require) (string, error) {
	for _, r := range require {
		key += "-" + filepath.Base(r.Module) + "-" + r.Version
	}
	binDir, err := BinDir(name, version, strings.TrimPrefix(key, "-"))
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
	if len(require) > 0 {
		if err := installRequiring(ctx, env, binDir, pkg, require); err != nil {
			return "", err
		}
		return bin, nil
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

// installRequiring builds pkg inside a throwaway module that requires the
// tool at its pinned version and each of require beside it. Minimal version
// selection then takes the higher of the tool's own requirement and ours, and
// the go command checks every module it fetches against the checksum database
// exactly as `go install pkg@version` does.
func installRequiring(ctx context.Context, env []string, binDir, pkg string, require []Require) error {
	work, err := os.MkdirTemp("", "lydite-install-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	// -mod=mod because the module is created empty and `go get` fills it in;
	// the default read-only mode would refuse to add the requirements.
	env = append(append([]string{}, env...), "GOBIN="+binDir, "GOFLAGS=-mod=mod")
	path, _, _ := strings.Cut(pkg, "@")
	steps := [][]string{{"mod", "init", "lydite.invalid/install"}, {"get", pkg}}
	for _, r := range require {
		steps = append(steps, []string{"get", r.Module + "@" + r.Version})
	}
	steps = append(steps, []string{"install", path})
	for _, args := range steps {
		if r := executil.RunEnv(ctx, work, env, "go", args...); !r.Ok() {
			return fmt.Errorf("installing %s: go %s: %w\n%s", pkg, args[0], r.Err, strings.TrimSpace(r.Output))
		}
	}
	return nil
}
