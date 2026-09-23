# A clearance carries forward when the decision it was given for is unchanged

[#243](https://github.com/lydite/lydite/issues/243), measured on `vipengele/typescript` #69: a
repository with a merge queue and a required `lydite/referral` can merge only changes that were
never referred. A clearance names exactly one commit and deliberately does not travel (ADR 0015);
a merge queue replays the change onto a fresh `main`, so the queue's commit is by construction a
revision nobody has cleared, and nobody can — a `gh-readonly-queue/` ref has no pull request, so
there is no comment surface `/lydite clear` can even in principle be typed at. Every referred
change is affected. `pedromvgomes/gt#79` tracks the same deadlock from gt 2.0, which makes
`lydite/referral` required on every governed repository.

Two constraints from the repository owner rule out the obvious fix. **lydite must not depend on
gt's attestation** — `lydite/actions` is consumed directly as well as through gt's stage, so
`gt/validated-tree` cannot be an input; any mechanism has to be lydite's own. **A merge queue
legitimately renders a different tree** — each entry is the change replayed on current `main`, so
tree equality often does not hold, and when it does not, a referral correctly re-raises against a
tree nobody has seen. Attesting the tree narrows the deadlock without removing it.

## Decision

**A clearance is not a statement about a tree. It is a statement about a decision, and it carries
forward exactly when that decision is unchanged.**

`internal/referral.Decide` refers a change for one of two reasons: an uncovered path
(`Uncovered`), or a disqualifier vetoing an otherwise-matching exemption (`Disqualifications`).
Both are pure functions of the diff and the exemptions file — no tree identity, no timestamp. A
rebase replays the same edits onto a newer base; the set of *reasons* `Decide` refers on is
normally identical, because it is a property of the change, not of the base.

So the queue-time question is not "is this the same tree" but "does `Decide`, recomputed on the
queue's own tree, refer for the same reasons it referred for when a human cleared it." Fingerprint
**`(Uncovered, Disqualifications)` together** — not `Uncovered` alone. `Uncovered` is only
populated on the branch where no single exemption covers every path (`decide.go:163`); a referral
caused purely by a disqualifier — a net-new suppression, a disabled test, an edit to
`.lydite/exemptions.yml` itself — leaves `Uncovered` empty, indistinguishable by that value alone
from "fully exempt." A fingerprint over `Uncovered` alone would republish success for a change
whose disqualifying evidence changed under rebase; over both, it does not.

The fingerprint is recomputed against **current `main`'s exemptions file**, never a pinned
revision of it. `Decide` already always reads exemptions from the merge-base, deliberately, so a
change gets no benefit from widening its own rules; pinning the exemptions revision at clear time
would let a clearance survive under rules that no longer apply, which ADR 0014's asymmetry
("an author-controlled claim may only ever add a referral, never remove one") already rules out
in spirit. If `.lydite/exemptions.yml` moved on `main` between clearing and queueing in a way that
changes the recomputed set, the fingerprint legitimately differs and the entry re-refers — that
is the existing "not equal" branch doing its job, not a new failure mode.

**No new durable store.** The queue ref names the PR's head SHA directly
(`gh-readonly-queue/main/pr-69-<sha>`), and GitHub commit statuses are keyed by SHA forever,
independent of which branch still points at that commit. The fingerprint is recorded in the
existing `lydite/clearance` status description, written at clear time as today
(`recordClearance`), budget-checked against the platform's 140-character cap alongside the
existing "cleared by @X" text already written there. At queue time the mechanism reads that same
description back off the head SHA — no `lydite` branch, no `contents: write` on a job whose whole
design is holding none. `#241`'s proposal to record finding detail on the `lydite` branch is
consequently uncoordinated with this and does not need to be.

**The relay learns a new shape; no direct-token job.** `pr-relay`'s `resolveTarget` admits no
`merge_group` claim today — every route is refused outright, because the queue ref does not parse
as a pull-ref and the one existing crossover (`clearanceRun`) is scoped to `issue_comment`. Rather
than mirror lydite's own `referral-publish` split (ADR 0051's exception, built because lydite
mirrors `lydite/actions` instead of consuming it), the relay gets a new admitted shape for
`merge_group`: given the OIDC claim, resolve the originating PR live from the queue ref, read the
`lydite/clearance` description off that PR's real head SHA (never the submitted claim alone — the
same live-resolution discipline every other relay route already applies), compare against a
freshly recomputed fingerprint the CI job submits, and write `lydite/referral` at the queue SHA.
This keeps the queue-time job — which still has to read diff evidence to recompute `Decide` — off
a `statuses: write` token, consistent with why the relay exists at all (ADR 0022).

**Attribution carries forward, not lydite's own.** The existing description already reads
`"cleared by @X at <sha>"`; on a match, the republished status at the queue SHA carries the same
attribution forward, because the human's judgement still applies — the decision they judged is
unchanged. No new identity field is needed.

**Not equal is not silence.** The relay publishes `lydite/referral` as `pending` with a
description naming why (what changed) — the same `StatePending` "standing referral, waiting on a
person" shape a normal referred-and-uncleared change already carries, and the one state
`clearance.Decide` accepts a `/lydite clear` comment against. `StateFailure`, the isolation gate,
is not this: `Decide` refuses to clear it for any comment, so publishing it here would make a
mismatched queue entry permanently unclearable and reopen the deadlock this ADR removes. The
entry drops out of the queue exactly as any required check still pending would; the author clears
again on the pull request and it re-enters. No new notification surface is needed — the existing
status-description text already explains itself the way it does today.

## Load-bearing parts, in landing order

1. `internal/referral`: a deterministic `Fingerprint(uncovered, disqualifications)` combinator.
2. `internal/clearance`: `recordClearance` embeds the fingerprint into the `lydite/clearance`
   description.
3. A `merge_group`-aware CLI path: recompute `Decide` against the queue tree and current `main`'s
   exemptions, derive the fingerprint, submit a comparison request to the relay instead of the
   current dead end (`base-branch` evaluates to `''` on `merge_group`, so nothing is published
   today).
4. `pr-relay`: the new `merge_group` shape — live PR resolution, live status read, compare,
   publish success-with-attribution or pending-with-reason at the queue SHA.
5. `lydite/actions`' `lydite.yml`: wire the `merge_group` trigger to call (3) instead of the
   current no-op.
6. `pedromvgomes/gt#79`: no code change of lydite's required; the issue closes as resolved by
   this design once shipped.

## Considered and rejected

**Tree or verdict equality** (ADR 0015's own framing, extended to "same referral answer travels")
— rejected there for requiring lydite to infer "these are the same change," an inference that is
wrong exactly when the tree legitimately differs, which a merge queue guarantees will happen. This
ADR's equivalence is narrower and evidence-based: not "the trees match" or "the answer happened to
match," but "the specific reasons `Decide` refers on are unchanged" — the one reading ADR 0015
left open, because it needs no inference about the change, only a recomputation of the same pure
function.

**Fingerprinting `Uncovered` alone**, as the handoff's initial sketch proposed — rejected once
research showed it is blind to every disqualifier-driven referral, which is not an edge case but
the mechanism behind most of what `Decide` actually refers on.

**Pinning the exemptions-file revision as part of the clearance's identity** — rejected; always
recomputing against current `main` is simpler, matches how `Decide` already always works, and
correctly re-refers when the rules genuinely changed, rather than resurrecting a clearance issued
under rules that no longer apply.

**The `lydite` branch as the fingerprint's store** — rejected; needs `contents: write` on a job
whose design deliberately excludes it, and is unnecessary once the head-SHA status is recognized
as already durable enough.

**A direct-token queue-republish job, mirroring `referral-publish`** — rejected as the general
mechanism; that split is ADR 0051's stated exception for lydite's own repo, not the shape ordinary
relay consumers should adopt. Extending the relay keeps the queue-time job, which still parses
diff evidence, off a writing token.

## The cheap half, not part of this decision

The same run that surfaces this deadlock also re-runs `scan`, `test` and `mutation` on the queue
commit while every gt stage beside it skips on the validated tree. Publishing the status needs
only the `referral` job, which has no `needs`. A `referral-only` input to `lydite.yml` is a
separate, adjacent fix — worth landing alongside this because it touches the same trigger, not
folded into this decision.

#243 and `pedromvgomes/gt#79` are not closed by this ADR alone; both close once the load-bearing
parts above ship.
