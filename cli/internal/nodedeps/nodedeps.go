// Package nodedeps decides how a JavaScript workspace's dependencies get
// installed, so the coverage gate and the test runner agree on the answer.
//
// A JavaScript suite cannot run at all without an install: a fresh checkout
// has no node_modules and every import fails before a single test is
// collected. Go and Rust have no equivalent step — their toolchains fetch
// what a build needs on the way past — which is why this exists for one
// language and not three.
//
// The rule lives here rather than in either caller because both ask the same
// question of the same tree. Two copies would answer it the same way until
// one of them learned about a package manager the other had not, at which
// point coverage and the suite would install differently in the same
// repository and only one of them would be wrong.
package nodedeps

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lydite/lydite/internal/executil"
)

// lockfiles maps each recognised lockfile to the package manager it
// identifies.
var lockfiles = map[string]string{
	"package-lock.json": "npm",
	"yarn.lock":         "yarn",
	"pnpm-lock.yaml":    "pnpm",
}

// Manager inspects root for exactly one recognised lockfile.
//
// More than one — usually a stale file nobody deleted — is ambiguous, and
// reports false rather than guessing a priority order. Installing with the
// wrong manager writes a lockfile the repository does not use, so the caller
// skipping the install is the smaller failure, and `typescript.install` in
// .lydite/config.yml is how a repository in that state says what it means.
func Manager(root string) (string, bool) {
	var found []string
	for file, manager := range lockfiles {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			found = append(found, manager)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// HasLockfile reports whether dir holds any recognised lockfile, ambiguous or
// not — the question a caller walking for workspace roots asks, where
// presence is what matters and not which single manager it names.
func HasLockfile(dir string) bool {
	for file := range lockfiles {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return true
		}
	}
	return false
}

// Managers lists every recognised package manager, sorted, for a message
// that tells the reader what would have been detected.
func Managers() []string {
	out := make([]string, 0, len(lockfiles))
	for _, m := range lockfiles {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Command is one step of an install.
type Command struct {
	// Argv is the program and its arguments, passed as argv with no shell —
	// except the override, which is a shell invocation by construction.
	Argv []string
	// Optional marks a step whose failure is not the install failing.
	// `corepack enable` is the case: it may already be enabled, or absent on
	// an older Node, and the install that follows works either way. Treating
	// it as fatal would turn a working repository into a failing one over a
	// step that had nothing to do.
	Optional bool
}

// Commands is what installing at root takes, in order, or nothing when no
// single manager could be identified.
//
// override, from typescript.install, replaces detection entirely and is run
// through a shell. A free-form user-authored command legitimately needs shell
// semantics (&&, env expansion), unlike lydite's own hardcoded tool
// invocations — and it is the only way to express a Corepack-pinned or
// otherwise nonstandard flow that no lockfile can imply.
//
// Every detected form is a *frozen* install: `npm ci`, `yarn --immutable`,
// `pnpm --frozen-lockfile`. An install that may rewrite the lockfile would
// have lydite silently change what the repository resolves to, and then
// measure and gate the result.
func Commands(root, override string) []Command {
	if override != "" {
		return []Command{{Argv: []string{"sh", "-c", override}}}
	}
	manager, ok := Manager(root)
	if !ok {
		return nil
	}
	switch manager {
	case "npm":
		return []Command{{Argv: []string{"npm", "ci"}}}
	case "yarn":
		return []Command{
			{Argv: []string{"corepack", "enable"}, Optional: true},
			{Argv: []string{"yarn", "install", "--immutable"}},
		}
	case "pnpm":
		return []Command{{Argv: []string{"pnpm", "install", "--frozen-lockfile"}}}
	default:
		return nil
	}
}

// Install runs the install for root, writing each command's output to out, and
// reports the first command that failed or nil when there was nothing to do.
//
// Whether a failure is fatal is the caller's to decide, and the two callers
// answer differently: the coverage gate omits a package it cannot measure,
// while a test run that proceeds after a failed install reports import errors
// naming the tests rather than the missing dependencies.
func Install(ctx context.Context, root, override string, out io.Writer) error {
	for _, cmd := range Commands(root, override) {
		// #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- every argv is built above from a fixed set, except the override, which comes from the target repo's own .lydite/config.yml and is authored by whoever configured lydite for that repo
		res := executil.RunOutput(ctx, root, nil, out, cmd.Argv[0], cmd.Argv[1:]...)
		if !res.Ok() && !cmd.Optional {
			return fmt.Errorf("%s: %w", strings.Join(cmd.Argv, " "), res.Err)
		}
	}
	return nil
}
