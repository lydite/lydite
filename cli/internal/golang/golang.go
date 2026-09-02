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
		results = append(results, executil.Result{Name: "gosec", Err: err})
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
		results = append(results, executil.Result{Name: "govulncheck", Err: err})
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
func ensure(ctx context.Context, env []string, name, version, pkg string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(cacheDir, "lydite", "gobin-"+name+"-"+version)
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
