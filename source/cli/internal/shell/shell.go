package shell

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/runner"
)

// Gate is the gate this package reports under. A gate's name is the label its
// row carries and the key its finding count is recorded under, so the two are
// one constant rather than two literals that agree until one is edited.
const Gate = "shellcheck"

// binary is what pipx puts on PATH for the pinned distribution.
const binary = "shellcheck"

// FindingGates is every gate here that reports its findings as data.
//
// It exists so a consumer can tell a gate that found nothing from one that
// never applied: a clean run reports no findings at all, so the set of gates a
// language implies is the only thing that makes a zero distinguishable from an
// absence. A fresh slice per call, because a package-level one is a variable
// every caller can edit.
func FindingGates() []string { return []string{Gate} }

// Check runs ShellCheck over every shell script under dir, failing on any
// diagnostic it reports.
//
// Results are named for the tool alone. Which component they belong to is the
// caller's to say.
//
// env.Install is what pipx is invoked with and env.Check what ShellCheck is.
// ShellCheck reads the scripts and never executes them, so the component's
// declared environment reaching it steers nothing but the options ShellCheck
// reads from SHELLCHECK_OPTS.
func Check(ctx context.Context, dir string, env executil.Env) []executil.Result {
	if r := ensure(ctx, env.Install); !r.Ok() {
		return []executil.Result{r}
	}
	scripts, err := scriptsUnder(ctx, dir)
	if err != nil {
		// Detail as well as Err: report() prints Detail under a failing row and
		// nothing else, so a bare Err renders as `✗ shellcheck` with the cause
		// in neither the terminal nor --json.
		return []executil.Result{{Name: Gate, Err: err, Detail: err.Error(), Crashed: true}}
	}
	if len(scripts) == 0 {
		// A failing row rather than a passing one. ShellCheck given no file
		// checks nothing, and a component that declares a language whose source
		// it does not hold is a declaration to correct, not a clean scan.
		err := fmt.Errorf("no shell script (%s) under the component's directory, so ShellCheck had nothing to check", strings.Join(runner.SourceExtsFor(runner.Shell), ", "))
		return []executil.Result{{Name: Gate, Err: err, Detail: err.Error(), Crashed: true}}
	}
	return []executil.Result{run(ctx, dir, env.Check, scripts)}
}

// argv is the one invocation, as argv.
//
// json1 rather than json: json counts a tab as advancing to the next multiple
// of eight, so its columns index a rendering of the line rather than the line,
// and the cut a site is made at would land in the wrong place on any indented
// script. json1 counts every character as one column.
//
// --norc, because ShellCheck otherwise reads a `.shellcheckrc` from each
// script's directory and every directory above it, then from the user's own
// config: a `disable=all` in any of them empties the report and passes the
// row, the one committed where no line of any script shows it, the other on
// the machine running the check, so a developer's run and CI's would disagree.
// A directive inside a script still applies, and is referral's to veto.
//
// Each script is passed as `./<path>`, so a file whose name begins with `-` is
// read as a file rather than as an option.
func argv(scripts []string) []string {
	args := []string{"--format=json1", "--norc"}
	for _, s := range scripts {
		args = append(args, "./"+s)
	}
	return args
}

// run runs ShellCheck once, as data.
//
// ShellCheck has no flag that writes a machine-readable report beside the
// human one: --format replaces the stream. So the JSON run is the only run —
// its exit status decides the row and its comments are the findings — and what
// a developer reads is the Detail report() prints from them.
// [lydite:exclude_from_coverage][a unit test here would run the machine's own
// shellcheck rather than lydite's invocation, which argv states and
// TestArgvIsOneJSON1RunOverEachScript asserts — everything done with the output
// is result, which the captured reports test directly]
func run(ctx context.Context, dir string, env []string, scripts []string) executil.Result {
	// RunQuiet, because the stream is JSON: streaming it would put a document
	// where a developer expects ShellCheck's own annotated source, which
	// arrives instead as the row's Detail.
	r := executil.RunQuietEnv(ctx, dir, env, binary, argv(scripts)...)
	r.Name = Gate
	return result(dir, r)
}

// scriptsUnder is every shell script git knows about under dir, relative to
// it, sorted.
//
// git's list rather than a walk, for the reason the orphan gate reads it: an
// ignored tree — node_modules, build output, a vendored checkout — is not the
// repository's own source, and a walk would lint it. A path git still lists
// and the tree no longer holds, or holds as something other than a regular
// file, is left out: ShellCheck handed a missing file exits 2 and checks
// nothing else it was given.
func scriptsUnder(ctx context.Context, dir string) ([]string, error) {
	tracked, err := gitdiff.Tracked(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("listing the shell scripts to check: %w", err)
	}
	exts := runner.SourceExtsFor(runner.Shell)
	var out []string
	for _, p := range tracked {
		if !slices.Contains(exts, strings.ToLower(path.Ext(p))) {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return out, nil
}

// ensure installs the pinned ShellCheck via pipx unless the shellcheck on PATH
// already reports that version, and refuses to go on if it still does not
// afterwards.
//
// The second check is what makes the pin a pin. pipx installs into its own bin
// directory, and a shellcheck earlier on PATH — a system package, a Homebrew
// install — is the one every later invocation resolves; an install that
// succeeded beneath it would otherwise report the row as run by the pinned
// version when it was not.
func ensure(ctx context.Context, env []string) executil.Result {
	want := upstreamVersion(version)
	if installedVersion(ctx) == want {
		return executil.Result{Name: Gate}
	}
	r := executil.RunEnv(ctx, "", env, "pipx", "install", "--force", pinnedPackage+"=="+version)
	// Override Name: executil.RunEnv sets it to the literal binary invoked
	// ("pipx"), but a failure here means the ShellCheck check itself never
	// ran — report() should say so, not "pipx".
	r.Name = Gate
	if !r.Ok() {
		r.Detail = fmt.Sprintf("installing %s==%s with pipx failed (%v)", pinnedPackage, version, r.Err)
		r.Crashed = true
		return r
	}
	if got := installedVersion(ctx); got != want {
		err := fmt.Errorf("pipx installed %s==%s, but the shellcheck on PATH reports %s rather than %s: another shellcheck ahead of pipx's bin directory is the one lydite would run", pinnedPackage, version, orNone(got), want)
		return executil.Result{Name: Gate, Err: err, Detail: err.Error(), Crashed: true}
	}
	return executil.Result{Name: Gate}
}

// installedVersion is the version the shellcheck on PATH reports, or empty
// when there is none or it will not say.
func installedVersion(ctx context.Context) string {
	if !executil.Available(binary) {
		return ""
	}
	v := executil.RunQuiet(ctx, "", binary, "--version")
	if !v.Ok() {
		return ""
	}
	return reportedVersion(v.Output)
}

// reportedVersion reads the `version: x.y.z` line of `shellcheck --version`.
func reportedVersion(out string) string {
	for line := range strings.Lines(out) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "version:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// upstreamVersion is the ShellCheck release a shellcheck-py version packages:
// its first three components. The fourth is the packaging revision, which
// `shellcheck --version` never prints, so comparing the whole pin against it
// would reinstall on every run.
func upstreamVersion(pinned string) string {
	parts := strings.Split(pinned, ".")
	return strings.Join(parts[:min(len(parts), 3)], ".")
}

// orNone names an empty version as the absence it is.
func orNone(v string) string {
	if v == "" {
		return "no version"
	}
	return v
}
