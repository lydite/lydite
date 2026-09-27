package reviewstages

import (
	"context"

	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// DecideIn is everything a review's decision is computed over.
type DecideIn struct {
	// Dir is the scan root, which locates the exemptions file and the
	// component declaration.
	Dir string
	// Base is the commit ResolveBase resolved.
	Base string
	// Surfaces is every opted-in component's comparison.
	Surfaces []reviewdecision.SurfaceComparison
	// EventPath is the pull request's event payload, already resolved by the
	// caller, whose title can declare a breaking change. Empty is no pull
	// request, which a local review has none of.
	EventPath string
	// Reports are the report directories whose scan documents supply the
	// licence and SCA evidence a conditional exemption needs.
	Reports []string
	// Scan reads a report directory's scan document.
	Scan reviewdecision.ScanReader
}

// DecideOut is the decision, and every warning computing it raised.
type DecideOut struct {
	Result reviewdecision.Result
	// Warnings are job-log lines, each without its newline, in the order they
	// arose: whatever the scan evidence and the title raised, in the order
	// reviewdecision.Decide asked for them, then Result.Warnings.
	Warnings []string
}

// Decide computes the review's decision (see reviewdecision.Decide), with the
// title and the scan evidence it asks for lazily read from this stage's own
// In.
//
// The warnings reading them raises are collected as each is asked for rather
// than written, so a caller writes them once, ahead of Result.Warnings, in the
// order they arose.
func Decide(ctx context.Context, in DecideIn) (DecideOut, error) {
	var warnings []string
	result, err := reviewdecision.Decide(ctx, reviewdecision.Input{
		Dir:      in.Dir,
		Base:     in.Base,
		Surfaces: in.Surfaces,
		Title: func() string {
			title, raised := reviewdecision.PullRequestTitle(in.EventPath)
			warnings = append(warnings, raised...)
			return title
		},
		ScanEvidence: func() bool {
			passed, raised := reviewdecision.ScanEvidence(in.Dir, in.Reports, in.Scan)
			warnings = append(warnings, raised...)
			return passed
		},
	})
	if err != nil {
		return DecideOut{}, err
	}
	return DecideOut{Result: result, Warnings: append(warnings, result.Warnings...)}, nil
}

// CheckDirtyIn names the checkout CheckDirty looks at.
type CheckDirtyIn struct {
	Dir string
}

// CheckDirtyOut reports whether the checkout has uncommitted changes.
type CheckDirtyOut struct {
	// Dirty is true when the working tree holds changes the decision, which
	// is computed over committed state alone, did not include.
	Dirty bool
}

// CheckDirty reports whether the checkout holds uncommitted changes. They are
// never part of the decision — it scores committed state, so the verdict is
// the one CI reaches — and a reader has to be told that the verdict does not
// cover them.
func CheckDirty(ctx context.Context, in CheckDirtyIn) (CheckDirtyOut, error) {
	return CheckDirtyOut{Dirty: referral.Dirty(ctx, in.Dir)}, nil
}
