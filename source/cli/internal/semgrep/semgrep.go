// Package semgrep runs Semgrep against a directory tree. Semgrep is a
// separate Python-packaged binary (not Rust or Go tooling), so unlike gosec/
// govulncheck it's installed via pipx rather than a language-native install
// command; lydite still ensures the pinned version is what's actually
// installed (not just "something called semgrep exists on PATH"), for the
// same toolchain-reproducibility reason as everything else it runs.
package semgrep

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/executil"
)

// AppTokenEnv is the environment variable Semgrep itself reads for AppSec
// Platform authentication. lydite doesn't invent its own — reusing
// Semgrep's own variable means a caller (e.g. the lydite/lydite GitHub
// Action) only has to plumb one secret through, and `semgrep ci` still works
// exactly as documented if invoked directly outside of lydite too.
const AppTokenEnv = "SEMGREP_APP_TOKEN" // #nosec G101 -- this is an env var NAME, not a credential value

// Gate is the gate this package reports under. A gate's name is the label its
// row carries and the key its finding count is recorded under, so the two are
// one constant rather than two literals that agree until one is edited.
//
// It is root-scoped: Semgrep runs once over the whole scan root rather than
// once per component, so its claims name no component and its count belongs to
// the repository rather than to any one of them.
const Gate = "semgrep"

// semgrepignoreFile is the filename Semgrep itself looks for at the root of a
// scan. Its presence — whatever it contains — replaces the built-in default
// ignore list below rather than extending it.
const semgrepignoreFile = ".semgrepignore"

// defaultIgnorePatterns is Semgrep's own default `.semgrepignore` template for
// the version pinned in requirements.txt: the paths Semgrep skips when the scan
// root carries no `.semgrepignore` of its own.
//
// Semgrep exposes no command that prints it. The list is the block following
// the marker string "default semgrepignore patterns" in the `semgrep-core`
// binary inside the pinned pip package, read with `strings`; a version bump
// must re-verify it the same way, because a pattern that has silently left
// Semgrep's template is one this warning names as dropped when it is not.
var defaultIgnorePatterns = []string{
	".git",
	".svn",
	".hg",
	"_darcs",
	"CVS",
	"build/",
	"vendor/",
	"dist/",
	"*.min.js",
	".env/",
	".tox/",
	"node_modules/",
	".npm/",
	".yarn/",
	".venv/",
	"_opam/",
	"_build/",
	"_cargo/",
	"test/",
	"tests/",
	"testsuite/",
	"*_test.go",
}

// warnSemgrepignore names what a `.semgrepignore` at the scan root costs.
//
// Semgrep treats the file as a replacement for its built-in defaults, not an
// addition to them, so a repository that writes one line to skip a directory
// also starts scanning every path in defaultIgnorePatterns — vendored code,
// build output, and the test trees `tests/` and `*_test.go` name. The findings
// count moves with no rule change and nothing in the report says why.
//
// A warning and not a row: the file is the repository's own configuration of
// its own scan, which ADR 0020 records as legitimate influence. Naming what is
// lost is the whole of what lydite owes here — it never rewrites the file, and
// the scope a consumer chose deliberately is not a failure.
func warnSemgrepignore(w io.Writer, dir string) {
	// Presence and not content: an empty file opts out of the whole template
	// just as a full one does, so stat answers the question and reading the
	// file would answer a different one.
	if _, err := os.Stat(filepath.Join(dir, semgrepignoreFile)); err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: %s at the scan root replaces Semgrep's built-in default ignore list rather than extending it, so Semgrep now scans %s — re-add the ones you want kept\n",
		semgrepignoreFile, strings.Join(defaultIgnorePatterns, ", "))
}

// Check runs Semgrep against dir using the given ruleset config (e.g. "auto",
// or a custom registry ref/path from .lydite/config.yml), failing on any finding.
//
// When SEMGREP_APP_TOKEN is set in the environment, this runs `semgrep ci`
// instead of `semgrep scan` — Semgrep's own diff-aware CI mode, which both
// scopes findings to what the current change actually introduced and
// uploads results to the Semgrep AppSec Platform dashboard, in one
// invocation. Without a token (local dev, but also any CI run GitHub
// withholds secrets from — a Dependabot PR being the standing example),
// behavior falls back to a plain `semgrep scan`.
//
// baseSHA, when non-empty, makes that fallback diff-aware too: Semgrep only
// reports findings absent at that commit. Empty means scan everything, which
// is what a local `lydite scan` wants.
//
// w carries the warnings about the environment the scan ran in — what a
// `.semgrepignore` at the scan root drops — and never the findings, which are
// the returned Result's.
func Check(ctx context.Context, dir, rulesetConfig, baseSHA string, w io.Writer) executil.Result {
	warnSemgrepignore(w, dir)
	if r := ensure(ctx); !r.Ok() {
		// A Semgrep that would not install scanned nothing, so the absence
		// of claims here is no answer about the code.
		r.Crashed = true
		return r
	}
	return withFindings(ctx, dir, buildArgs(rulesetConfig, os.Getenv(AppTokenEnv) != "", baseSHA))
}

// buildArgs decides the semgrep subcommand and flags: `ci` (diff-aware,
// uploads to the AppSec Platform) when appToken is set, otherwise a plain
// `scan`. `ci` mode omits --config entirely for the "auto" sentinel, since
// semgrep ci already applies the org's configured platform policy by
// default — passing "--config auto" would override that with the plain
// community ruleset instead of layering on top of it. It also ignores
// baseSHA: `semgrep ci` already derives its own diff base from the CI
// environment, and passing --baseline-commit on top of that is redundant.
//
// The `scan` fallback takes --baseline-commit so that, in a PR, it blocks on
// the same thing `semgrep ci` would — findings the PR itself introduces —
// rather than on every pre-existing finding in the repo. Without this, whether
// a PR is green depends on whether the run happened to have a token, which is
// not a property of the code under review.
func buildArgs(rulesetConfig string, appToken bool, baseSHA string) []string {
	if appToken {
		args := []string{"ci"}
		if rulesetConfig != "" && rulesetConfig != "auto" {
			args = append(args, "--config", rulesetConfig)
		}
		return args
	}
	args := []string{"scan", "--config", rulesetConfig, "--error"}
	if baseSHA != "" {
		args = append(args, "--baseline-commit", baseSHA)
	}
	return args
}

// ensure installs the pinned Semgrep version via pipx unless it's already
// installed at exactly that version.
func ensure(ctx context.Context) executil.Result {
	if executil.Available("semgrep") {
		v := executil.Run(ctx, "", "semgrep", "--version")
		if v.Ok() && strings.TrimSpace(v.Output) == version {
			return executil.Result{Name: Gate}
		}
	}
	r := executil.Run(ctx, "", "pipx", "install", "--force", "semgrep=="+version)
	// Override Name: executil.Run sets it to the literal binary invoked
	// ("pipx"), but a failure here means the Semgrep check itself never ran —
	// report() should say so, not "pipx".
	r.Name = Gate
	return r
}
