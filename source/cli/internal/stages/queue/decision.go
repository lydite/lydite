package queuestages

import (
	"context"

	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// ResolveBaseIn is what the base the decision is recomputed against is
// resolved from, as reviewdecision.ResolveBase takes it.
type ResolveBaseIn struct {
	// Dir is the checkout the decision is recomputed over.
	Dir string
	// Base is a commit, or "auto" for the merge-base with BaseBranch.
	Base string
	// BaseBranch is the branch the entry is queued for.
	BaseBranch string
}

// ResolveBaseOut is the commit the decision is recomputed against.
type ResolveBaseOut struct {
	BaseSHA string
}

// ResolveBase resolves the base from the checkout rather than taking the
// payload's own base_sha, for the reason review resolves its own: the revision
// the diff is read against decides what the decision is computed over, and
// this run can establish it from the checkout it holds.
func ResolveBase(ctx context.Context, in ResolveBaseIn) (ResolveBaseOut, error) {
	baseSHA, err := reviewdecision.ResolveBase(ctx, in.Dir, in.Base, in.BaseBranch)
	if err != nil {
		return ResolveBaseOut{}, err
	}
	return ResolveBaseOut{BaseSHA: baseSHA}, nil
}

// RecomputeDecisionIn is the checkout and the base the entry's decision is
// recomputed over.
type RecomputeDecisionIn struct {
	Dir     string
	BaseSHA string
}

// RecomputeDecisionOut is the recomputed decision and what is submitted of it.
type RecomputeDecisionOut struct {
	Decision referral.Decision
	// Fingerprint is referral.Fingerprint over the reasons Decision refers
	// on, and the whole of what the relay compares.
	Fingerprint string
	// Referred is whether Decision refers at all. A decision that refers
	// for nothing has no clearance to carry forward, which a fingerprint
	// alone cannot say.
	Referred bool
}

// RecomputeDecision recomputes the decision this entry renders:
// reviewdecision.DecideFromDiff, over the diff and the base commit's
// exemptions alone.
//
// The base is the base branch's current tip at queue time, so the exemptions
// are the ones in force now: a clearance carried forward under rules that have
// since moved would be a clearance issued under rules that no longer apply. If
// the file moved in a way that changes the recomputed set, the fingerprint
// legitimately differs and the entry re-refers.
func RecomputeDecision(ctx context.Context, in RecomputeDecisionIn) (RecomputeDecisionOut, error) {
	decision, err := reviewdecision.DecideFromDiff(ctx, in.Dir, in.BaseSHA)
	if err != nil {
		return RecomputeDecisionOut{}, err
	}
	return RecomputeDecisionOut{
		Decision:    decision,
		Fingerprint: referral.Fingerprint(decision.Uncovered, decision.Disqualifications),
		Referred:    decision.Referred,
	}, nil
}
