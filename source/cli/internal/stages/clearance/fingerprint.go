package clearancestages

import (
	"context"
	"fmt"
	"io"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// FingerprintIn is what the cleared decision is recomputed from.
type FingerprintIn struct {
	// Repository resolves the pull request's title, live.
	Repository forge.SCMRepository
	// Dir is the checkout the decision is recomputed over, which has to be
	// at Head.
	Dir string
	// Base and BaseBranch resolve the commit the change is measured against,
	// as reviewdecision.ResolveBase takes them.
	Base       string
	BaseBranch string
	// SurfacesDocument names a comparison `review compare` already wrote, and
	// is empty for a run that makes the comparison itself.
	SurfacesDocument string
	Number           int
	// Head is the revision being cleared.
	Head string
	// Toolchains provisions each opted-in component's toolchain when the
	// comparison is made here.
	Toolchains reviewdecision.Toolchains
	// Progress is where a comparison's own tool reports what it is doing. A
	// nil Progress discards it.
	Progress io.Writer
}

// FingerprintOut is the fingerprint of the cleared decision, empty when it
// could not be computed.
type FingerprintOut struct {
	Fingerprint string
	// Warnings are job-log lines, each without its newline, in the order they
	// arose.
	Warnings []string
}

// Fingerprint is the fingerprint of the decision this clearance is given for.
//
// Empty is a recorded answer rather than a silent one: clearance.WithFingerprint
// appends nothing, which clearance.FingerprintIn reads back as absent, and the
// relay refuses a comparison it cannot make rather than treating absence as
// agreement — so a clearance recorded without one still resolves the referral
// on this head and simply does not carry onto a merge-queue entry. Every way
// that can happen is named in Warnings, because a clearance that quietly stops
// travelling is one nobody can tell from a queue entry that legitimately
// re-refers.
//
// It never returns an error. Answering the comment is the job, and a
// fingerprint that could not be taken is not a reason to leave the referral
// standing with the commenter told nothing.
func Fingerprint(ctx context.Context, in FingerprintIn) (FingerprintOut, error) {
	if in.Progress == nil {
		in.Progress = io.Discard
	}
	decision, warnings, err := clearedDecision(ctx, in)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf(
			"lydite: this clearance records no fingerprint, so it will not carry onto a merge-queue entry: %v", err))
		return FingerprintOut{Warnings: warnings}, nil
	}
	return FingerprintOut{
		Fingerprint: referral.Fingerprint(decision.Uncovered, decision.Disqualifications),
		Warnings:    warnings,
	}, nil
}

// clearedDecision recomputes, for the revision being cleared, the decision a
// person is clearing, with every warning raised along the way.
//
// It is `review`'s own computation and not a summary of it: the exemptions at
// the base commit, the change's own diff, the API-surface comparison every
// opted-in component asked for, and the dependency comparison for every
// manifest the change touches — because the fingerprint has to describe the
// decision that was cleared. A narrower one would record a person's judgement
// against reasons that were never the whole of what referred the change.
//
// in.SurfacesDocument names a comparison `review compare` already made, and is
// the route for a component whose comparison runs the change's own code: read
// here, it is reconciled against what this run resolves for itself and never
// re-run, so the job holding the credential this clearance is recorded with
// executes none of it.
//
// No scan evidence is given, so every `versions:` condition fails — the same
// answer `clearance queue` recomputes under: a condition needs the licence and
// SCA rows of a scan document, which no comment-answering job has, and passing
// nothing is the direction that refers.
func clearedDecision(ctx context.Context, in FingerprintIn) (referral.Decision, []string, error) {
	var warnings []string
	// Cheapest first, and refused before anything is resolved or fetched: a
	// decision computed over some other revision is not the decision being
	// cleared, and recording its fingerprint would attach a person's judgement
	// to reasons that were never in front of them.
	if err := checkoutIsHead(ctx, in.Dir, in.Head); err != nil {
		return referral.Decision{}, warnings, err
	}
	baseSHA, err := reviewdecision.ResolveBase(ctx, in.Dir, in.Base, in.BaseBranch)
	if err != nil {
		return referral.Decision{}, warnings, err
	}
	// Always guarded, unlike `review`'s own fallback: `review`'s guard tests
	// whether the invocation also publishes, because a `review` run that only
	// renders holds no credential of its own. A clearance run needs a
	// credential to read the head, the commenter's permission and the
	// standing referral, and to post the reply, so it holds one whether or
	// not it also posts the status directly, and there is no invocation in
	// which running a component's own build code here is safe. A document is
	// the only route to a full-parity comparison for a component whose
	// comparison runs the change's own code, and an unreadable one makes
	// every opted-in component uncomputable — the decision `review` reaches
	// from the same document, which the fingerprint has to describe rather
	// than a cleaner reading of it; see
	// agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md.
	surfaces, err := reviewdecision.Surfaces(ctx, in.Dir, baseSHA, in.SurfacesDocument, true, in.Toolchains, in.Progress)
	if err != nil {
		return referral.Decision{}, warnings, err
	}
	result, err := reviewdecision.Decide(ctx, reviewdecision.Input{
		Dir:      in.Dir,
		Base:     baseSHA,
		Surfaces: surfaces,
		// Resolved live rather than read from the comment: an issue_comment
		// event carries no pull_request.title at all, only the comment
		// thread's own issue.title, which is the pull request's title only by
		// convention. A break declared solely in the title would otherwise
		// never be seen here. A failure to resolve it can only under-refer,
		// so it is warned about rather than fatal.
		Title: func() string {
			title, err := in.Repository.PullRequestTitle(ctx, in.Number)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf(
					"lydite: could not resolve the pull request's title (%v) — a break declared only there is not seen", err))
			}
			return title
		},
	})
	if err != nil {
		return referral.Decision{}, warnings, err
	}
	return result.Decision, append(warnings, result.Warnings...), nil
}

// checkoutIsHead establishes that the tree the decision is recomputed over is
// the revision being cleared.
//
// A job answering a comment is free to check out anything, and the default
// branch is the ordinary choice: the change being cleared is not in that tree
// at all, so a recomputation there answers for the default branch and
// fingerprints a decision nobody was asked about.
func checkoutIsHead(ctx context.Context, dir, head string) error {
	r := executil.RunQuiet(ctx, dir, "git", "rev-parse", "HEAD")
	at := strings.TrimSpace(r.Output)
	if !r.Ok() || at == "" {
		return fmt.Errorf("reading which revision %s is checked out at: %w", dir, r.Err)
	}
	if at != head {
		return fmt.Errorf("the checkout is at %s, not the revision being cleared (%s) — "+
			"the decision has to be recomputed against the pull request's own head", shortSHA(at), shortSHA(head))
	}
	return nil
}
