// Package secrets runs gitleaks once over the scan root's working tree, so a
// credential committed to any file — a workflow, a compose file, a fixture, an
// .env somebody added — is found rather than left to gosec's G101, which sees
// only Go source.
//
// It is root-scoped, like Semgrep and unlike every language check: a secret
// scanner reads bytes rather than a build graph, and the files most likely to
// carry a credential belong to no component at all. So its claims name no
// component and their count belongs to the repository. See ADR 0035.
//
// The working tree and not the history. `gitleaks git` reads every blob in
// every reachable commit, which needs a full clone from every consumer and
// counts secrets that no working file holds — a number that can only rise and
// a claim with no line to anchor to. What that gives up is stated rather than
// buried: a credential committed and later deleted stays leaked and lydite
// never mentions it.
//
// And the part of that working tree git would carry. gitleaks walks every file
// under the scan root, so a warm target/ or an installed node_modules/ is read
// as source and its compiled-in test vectors are reported as leaks; no flag
// scopes that walk, so the claims are scoped instead, against what
// gitdiff.Tracked answers. A credential in a file git will not carry cannot be
// committed by accident, which is the leak this gate exists to catch — and a
// file that is untracked but not ignored is one `git add .` from being
// published, so it stays in scope. A real credential sitting in ignored output
// is what that gives up.
//
// Two answers from git are not a filter. A nested repository — a submodule, or
// an embedded one — is where ls-files stops and gitleaks does not, so
// everything under it is kept rather than dropped unexamined; and a root git
// lists no file at all under is a scope lydite never established, which fails
// the row beside a report naming a leak instead of filtering every one of them
// away.
package secrets

import (
	"context"
	"os"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gotool"
)

// Gate is the gate this package reports under. A gate's name is the label its
// row carries and the key its finding count is recorded under, so the two are
// one constant rather than two literals that agree until one is edited.
//
// It names the tool, as `gosec` and `semgrep` do: a row names what a reader
// would re-run. The configuration key that switches it off is named for the
// concern instead, because a repository declining this is declining secret
// scanning rather than a vendor.
const Gate = "gitleaks"

// FindingGates is every gate here that reports its findings as data.
//
// It exists so a consumer can tell a gate that found nothing from one that
// never ran: a clean run reports no findings at all, so the set of gates a
// scan implies is the only thing that makes a zero distinguishable from an
// absence. A fresh slice per call, because a package-level one is a variable
// every caller can edit.
func FindingGates() []string { return []string{Gate} }

// Check runs the pinned gitleaks over dir's working tree.
//
// The install runs with lydite's own environment and never a scanned
// repository's: `go install` reads GOPROXY and GOSUMDB, so a declaration
// reaching it would choose where lydite's scanner comes from. It is keyed by
// the tool's version alone and not by any component's Go toolchain, because
// gitleaks analyses no Go source — the same distinction that keeps gotestsum's
// pin out of go-pin.
func Check(ctx context.Context, dir string) executil.Result {
	bin, err := gotool.Ensure(ctx, nil, "gitleaks", gitleaksVersion, gitleaksPkg, "")
	if err != nil {
		// Detail as well as Err: report() prints Detail under a failing row and
		// nothing else, so a tool that would not install renders as a bare
		// `✗ gitleaks` with the cause in neither the terminal nor --json.
		return executil.Result{Name: Gate, Err: err, Detail: err.Error(), Crashed: true}
	}
	return run(ctx, dir, bin)
}

// argv is the one invocation, as argv.
//
// Three flags are load-bearing and none is a preference. --verbose is what
// makes gitleaks print its findings at all; without it a failing run says
// `leaks found: 61` and names no location. --redact replaces the matched text
// with REDACTED in the terminal output and in the report alike, so neither a CI
// log nor an uploaded report carries a credential. --report-path writes a
// *copy*, which is what keeps the human stream and the exit status untouched
// and the check to one pass. --no-banner keeps the ASCII art out of the log.
//
// No --config: gitleaks discovers a repository's own .gitleaks.toml at the scan
// root, which is where a repository's allowlist belongs. Passing one would beat
// the scanned repository's file, which ADR 0020 records as a limitation of the
// Biome invocation rather than a model to copy.
func argv(reportPath string) []string {
	return []string{
		"dir",
		"--no-banner", "--redact", "--verbose",
		"--report-format", "json", "--report-path", reportPath,
		".",
	}
}

// run runs gitleaks and reads a JSON copy of its report.
//
// The tool keeps printing its own findings, so Result.Detail stays empty except
// for what lydite itself has to say, and only Result.Findings is new.
// [lydite:exclude_from_coverage][the self-scan runs gitleaks over the whole
// repository on every run; a unit test here would run the machine's own gitleaks
// rather than lydite's invocation, which argv states and TestArgvKeepsTheReportACopy
// asserts, over a report result() reads and TestResult* pin]
func run(ctx context.Context, dir, bin string) executil.Result {
	out, err := os.CreateTemp("", "lydite-gitleaks-*.json")
	if err != nil {
		return executil.Result{Name: Gate, Err: err, Detail: err.Error(), Crashed: true}
	}
	reportPath := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(reportPath) }()

	r := executil.Run(ctx, dir, bin, argv(reportPath)...)
	r.Name = Gate

	// Asked after the walk rather than before it: gitleaks' own output and exit
	// status are what a reader sees first, and a git that will not answer is a
	// row this gate fails with the reason rather than a run it declines to
	// make.
	keep, scopeErr := tracked(ctx, dir)
	// Decided before result replaces gitleaks' own exit with lydite's verdict,
	// since the status is half of what says whether the walk finished.
	incomplete := crashed(r.Err, reportPath, scopeErr)
	r = result(r, dir, reportPath, keep, scopeErr)
	r.Crashed = incomplete
	return r
}

// crashed reports whether a run's claims cannot be read as the whole of what
// the tree holds.
//
// The three outcomes result names as this gate failing rather than finding —
// no report lydite could read, a walk gitleaks' own status says it did not
// finish, and a scope git could not be asked for, whose claims include ignored
// output a scoped run would drop. Each leaves a set of claims whose difference
// from the last run's says nothing about the tree. A leak lydite could not
// place is not one: it fails the row, but the walk was whole, and a leak with
// no line had no fingerprint to resolve in the first place.
func crashed(exit error, reportPath string, scopeErr error) bool {
	rep, readErr := readReport(reportPath)
	if readErr != nil {
		return true
	}
	return !ranToCompletion(exit, rep) || scopeFailure(scopeErr, rep) != nil
}
