package scanstages

import (
	"context"
	"io"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/secrets"
	"lydite/lydite/internal/semgrep"
)

// SemgrepIn is the scan root Semgrep runs over, and what its diff-aware
// fallback needs to scope itself the same way `semgrep ci` would.
type SemgrepIn struct {
	// Dir is the scan root Semgrep runs against, whole and unconditionally —
	// unlike every language check, Semgrep runs once over the tree as a
	// whole whatever the declaration says.
	Dir string
	// BaseSHA is the resolved diff base, empty for a scan given none.
	BaseSHA string
	// SemgrepAppToken reports whether SEMGREP_APP_TOKEN is set. `semgrep ci`
	// already scopes itself to the diff, so a set token means BaseSHA never
	// reaches Semgrep — see semgrepBase. Read by the caller, not this stage:
	// no stage reads the process environment.
	SemgrepAppToken bool
	// Config is the ruleset Semgrep runs — cfg.Semgrep.Config from
	// .lydite/config.yml, "auto" by default.
	Config string
	// Changed is, per path from the scan root, the lines the change touched,
	// which decides each claim's anchor. Empty for a scan with no diff base.
	Changed map[string][]int
	// Diagnostics is where Semgrep's own warnings are written — naming what
	// a .semgrepignore at the scan root drops from its default ignore list —
	// at the moment they arise, never returned for a caller to print later.
	Diagnostics io.Writer
}

// SemgrepOut is Semgrep's outcome over the whole scan root.
type SemgrepOut struct {
	// Result is Semgrep's row.
	Result executil.Result
	// Findings is Result's located claims, anchored against the lines the
	// change touched. Semgrep names no component — it is root-scoped and
	// component-independent — so neither does a claim here.
	Findings []finding.Finding
	// Crashes is Result as a crashed bucket, where Semgrep's own report says
	// its claims are not a complete answer.
	Crashes []finding.Crash
}

// Semgrep runs Semgrep once over the scan root. It runs under the flow's own
// When(SemgrepEnabled), so this stage does not read the switch itself.
//
// Semgrep's outcome is data in the Out, never this stage's error: a scanner
// that would not install and one that found something both report through
// Result, exactly as every language check's do.
func Semgrep(ctx context.Context, in SemgrepIn) (SemgrepOut, error) {
	return semgrepStage(ctx, in, semgrep.Check)
}

// semgrepCheck is Semgrep's own invocation, as a function so a test can
// observe what it was called with — the diagnostics writer, and the base
// semgrepBase resolved for the token flag — without a real Semgrep install.
type semgrepCheck func(ctx context.Context, dir, rulesetConfig, baseSHA string, w io.Writer) executil.Result

// semgrepStage is Semgrep with the invocation supplied.
func semgrepStage(ctx context.Context, in SemgrepIn, check semgrepCheck) (SemgrepOut, error) {
	result := check(ctx, in.Dir, in.Config, semgrepBase(in.BaseSHA, in.SemgrepAppToken), in.Diagnostics)
	results := []executil.Result{result}
	return SemgrepOut{
		Result:   result,
		Findings: anchoredFindings(results, in.Changed),
		Crashes:  crashesOf(results, ""),
	}, nil
}

// SecretsIn is the scan root gitleaks walks.
type SecretsIn struct {
	// Dir is the scan root, walked whole — like Semgrep and unlike every
	// language check, a secret scanner reads bytes rather than a build
	// graph, and the files most likely to carry a credential belong to no
	// component at all.
	Dir string
	// Changed is, per path from the scan root, the lines the change touched,
	// which decides each claim's anchor. Empty for a scan with no diff base.
	//
	// gitleaks itself takes no base — it has no --baseline-commit — so every
	// secret in the tree is a claim; this only decides which of them reach a
	// line of the change and which land in the standing comment as
	// pre-existing debt.
	Changed map[string][]int
}

// SecretsOut is gitleaks's outcome over the scan root's working tree.
type SecretsOut struct {
	// Result is gitleaks's row.
	Result executil.Result
	// Findings is Result's located claims, anchored against the lines the
	// change touched. gitleaks names no component — it is root-scoped and
	// component-independent — so neither does a claim here.
	Findings []finding.Finding
	// Crashes is Result as a crashed bucket, where gitleaks's own report
	// says its claims are not the whole of what the tree holds.
	Crashes []finding.Crash
}

// Secrets runs gitleaks once over the scan root's working tree — the part of
// it git would carry, never history. It runs under the flow's own
// When(SecretsEnabled), so this stage does not read the switch itself.
//
// gitleaks's outcome is data in the Out, never this stage's error.
func Secrets(ctx context.Context, in SecretsIn) (SecretsOut, error) {
	result := secrets.Check(ctx, in.Dir)
	results := []executil.Result{result}
	return SecretsOut{
		Result:   result,
		Findings: anchoredFindings(results, in.Changed),
		Crashes:  crashesOf(results, ""),
	}, nil
}
