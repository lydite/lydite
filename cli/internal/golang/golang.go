// Package golang runs gosec and govulncheck against every Go module found
// under the scan root. Both are
// installed via `go install` into a lydite-managed, version-keyed bin
// directory (never trusting whatever gosec/govulncheck might already be on
// PATH) — the same "pin the exact toolchain, don't reuse ambient installs"
// principle as the TypeScript toolchain, just using Go's own install
// mechanism instead of npx/npm.
package golang

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/executil"
)

// Pinned so every invocation of lydite uses the exact same toolchain
// regardless of what's already on the machine.
const (
	gosecVersion       = "v2.29.0"
	govulncheckVersion = "v1.7.0"

	gosecPkg       = "github.com/securego/gosec/v2/cmd/gosec@" + gosecVersion
	govulncheckPkg = "golang.org/x/vuln/cmd/govulncheck@" + govulncheckVersion
)

// Check runs gosec and govulncheck in dir, with env on top of the caller's
// own environment — the component's resolved Go toolchain.
//
// Both tools are module-scoped: govulncheck exits with "no go.mod file" when
// run anywhere but a module root, which is why a Go component's directory has
// to be its module root. It is the same requirement internal/coverage already
// states for the same reason, and a component declared elsewhere fails here
// with govulncheck naming it.
//
// Results are named for the tool alone. Which component they belong to is the
// caller's to say.
func Check(ctx context.Context, dir string, env []string) []executil.Result {
	var results []executil.Result

	if bin, err := ensure(ctx, env, "gosec", gosecVersion, gosecPkg); err != nil {
		// Detail as well as Err: report() prints Detail under a failing row
		// and nothing else, so a tool that would not install renders as a
		// bare `✗ gosec` with the cause in neither the terminal nor --json.
		results = append(results, executil.Result{Name: "gosec", Err: err, Detail: err.Error()})
	} else {
		// -exclude-generated skips files carrying the standard
		// "Code generated ... DO NOT EDIT." header. Findings there are not
		// actionable: the only fix is to change the generator or its input,
		// and a `#nosec` annotation would be erased by the next regeneration.
		// This matches how generated code is already treated elsewhere in the
		// pipeline — golangci-lint's `exclusions: generated` and semgrep's own
		// generated-file skip.
		r := executil.RunEnv(ctx, dir, env, bin, "-exclude-generated", "./...")
		r.Name = "gosec"
		results = append(results, r)
	}

	if bin, err := ensure(ctx, env, "govulncheck", govulncheckVersion, govulncheckPkg); err != nil {
		results = append(results, executil.Result{Name: "govulncheck", Err: err, Detail: err.Error()})
	} else {
		r := executil.RunEnv(ctx, dir, env, bin, "./...")
		r.Name = "govulncheck"
		results = append(results, r)
	}

	return results
}

// ensure installs pkg via `go install` into a version-keyed lydite cache
// directory (GOBIN), so a version bump gets a fresh install instead of
// silently reusing a stale one, and returns the path to the installed binary.
//
// The key carries the Go toolchain as well as the tool's version, because the
// toolchain is per component: a repository declaring `go 1.24` in one module
// and `go 1.28` in another builds this tool twice, and a single key would let
// whichever component ran first decide which build every other one gets. A
// tool built by an older Go rejects newer source outright — the failure
// GOTOOLCHAIN is pinned to prevent — and keyed on the tool alone the verdict
// would depend on declaration order.
func ensure(ctx context.Context, env []string, name, version, pkg string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(cacheDir, "lydite", "gobin-"+name+"-"+version+"-"+toolchainKey(env))
	bin := filepath.Join(binDir, name)

	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return "", err
	}
	r := executil.RunEnv(ctx, "", append(append([]string{}, env...), "GOBIN="+binDir), "go", "install", pkg)
	if !r.Ok() {
		return "", r.Err
	}
	return bin, nil
}

// toolchainKey names the Go toolchain a build will use, for the cache
// directory. It reads GOTOOLCHAIN out of the environment the install is about
// to run with, since that is what decides the answer; a component with none
// resolved shares the "ambient" key, which is the one toolchain such
// components all get.
func toolchainKey(env []string) string {
	value := "ambient"
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOTOOLCHAIN="); ok && v != "" {
			value = v
		}
	}
	// A directory name, so anything that is not plainly a name becomes a
	// dash: GOTOOLCHAIN accepts forms like "go1.26.0+auto", and a path
	// separator in a cache key would silently nest the install somewhere else.
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.':
			return r
		default:
			return '-'
		}
	}, value)
}
