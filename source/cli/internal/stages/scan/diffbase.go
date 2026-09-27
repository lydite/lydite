package scanstages

import (
	"context"
	"fmt"
	"strings"

	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
)

// ResolveDiffBaseIn is the --diff-base flag as the caller wrote it, and what
// "auto" is resolved against.
type ResolveDiffBaseIn struct {
	// Dir is the scan root, where git is run.
	Dir string
	// DiffBase is the flag's value: empty, "auto", or a ref.
	DiffBase string
	// BaseBranch overrides the branch "auto" computes the merge-base with,
	// and is empty to let gitstate choose it.
	BaseBranch string
}

// ResolveDiffBaseOut is the commit the scan is scoped to, or "" for a scan of
// everything.
type ResolveDiffBaseOut struct {
	SHA string
}

// ResolveDiffBase turns the --diff-base flag into a commit SHA for Semgrep's
// scan-mode --baseline-commit. "auto" resolves the same merge-base
// `lydite coverage` already gates against, so a PR's scan and coverage agree
// on what "this change" means; any other non-empty value is passed through as
// a literal ref.
//
// It is resolved whenever --diff-base is given, including for a run carrying a
// SEMGREP_APP_TOKEN and a run with Semgrep switched off. The base has a second
// reader besides Semgrep: it is what decides whether a finding reaches a line
// of the change, and so whether it becomes a review thread or a row in the
// standing comment. A token says `semgrep ci` scopes itself; it says nothing
// about where gosec's claims belong.
//
// The cost is real and is the point: every consumer passing `--diff-base
// auto` needs a full-history checkout to resolve the merge-base, a token in
// the environment notwithstanding. Asking for a diff-scoped scan and getting
// claims that reach no line is what the alternative buys.
//
// Its failure is an error rather than a row, so it belongs before any check
// runs rather than after every one has: resolving it late would discard every
// finding already collected — minutes of clippy and gosec traded for one
// sentence about a shallow checkout.
func ResolveDiffBase(ctx context.Context, in ResolveDiffBaseIn) (ResolveDiffBaseOut, error) {
	diffBase := in.DiffBase
	if diffBase == "" {
		return ResolveDiffBaseOut{}, nil
	}
	if diffBase == "auto" {
		// Deliberately an error, not a silent full-repo scan: falling back
		// would reintroduce exactly the surprise this flag exists to remove —
		// a scan that quietly changes scope, and starts blocking on findings
		// the PR never touched. A shallow checkout is a fixable CI
		// misconfiguration (fetch-depth: 0), so say so.
		baseSHA, err := gitstate.ResolveBaseSHA(ctx, in.Dir, in.BaseBranch)
		if err != nil {
			return ResolveDiffBaseOut{}, fmt.Errorf("--diff-base auto: %w (a full-history checkout is required — set fetch-depth: 0)", err)
		}
		diffBase = baseSHA
	}
	// Resolved to a commit before it reaches anything, so what a tool is given
	// is a SHA and never the caller's string. The anchor reads this base by
	// handing it to `git diff <base>..HEAD`, where a value beginning with `-`
	// is a position git parses as an option — `--diff-base --output=/tmp/x`
	// would make git write the diff to a path of the caller's choosing.
	// --end-of-options is what stops that here, and resolving once is what
	// makes verifying it here sufficient for every later invocation.
	// reviewdecision.ResolveBase does the same for --base, and executil.RunQuiet's
	// own doc names this as the caller's duty.
	rev := executil.RunQuiet(ctx, in.Dir, "git", "rev-parse", "--verify", "--quiet", "--end-of-options", diffBase+"^{commit}")
	resolved := strings.TrimSpace(rev.Output)
	if !rev.Ok() || resolved == "" {
		return ResolveDiffBaseOut{}, fmt.Errorf("--diff-base %q does not name a commit", diffBase)
	}
	return ResolveDiffBaseOut{SHA: resolved}, nil
}

// ReadChangedLinesIn names the scan root and the commit the change is
// measured from.
type ReadChangedLinesIn struct {
	Dir string
	// BaseSHA is the resolved diff base, empty for a scan of everything.
	BaseSHA string
}

// ReadChangedLinesOut is, per path from the scan root, the lines the change
// added or modified. It is empty when there is no base.
type ReadChangedLinesOut struct {
	Changed map[string][]int
}

// ReadChangedLines reads the lines the change touched, so a claim on one
// becomes a review thread rather than a row in the standing comment. Asked
// once and partitioned by path afterwards: every check measures the same
// range, and asking git per component is the same answer computed N times.
//
// Empty without a base, which leaves every claim unanchorable — the honest
// answer for a scan over a whole repository, which reaches no change at all.
// It answers empty rather than being skipped, because every stage anchoring a
// claim reads it whether or not there was a base.
func ReadChangedLines(ctx context.Context, in ReadChangedLinesIn) (ReadChangedLinesOut, error) {
	if in.BaseSHA == "" {
		return ReadChangedLinesOut{}, nil
	}
	changed, err := coverage.ChangedLines(ctx, in.Dir, in.BaseSHA)
	if err != nil {
		return ReadChangedLinesOut{}, err
	}
	return ReadChangedLinesOut{Changed: changed}, nil
}

// semgrepBase is the diff base Semgrep is given, which is none when a Semgrep
// token is set.
//
// `semgrep ci` scopes itself to the diff already, and passing --baseline-commit
// on top of that is redundant. The rule lives here rather than in the resolver
// because the resolved base has a second reader: a finding's anchor, which a
// Semgrep token says nothing about. Whether the token is set is the caller's to
// say, so nothing here reads the environment for it.
func semgrepBase(baseSHA string, appTokenSet bool) string {
	if appTokenSet {
		return ""
	}
	return baseSHA
}
