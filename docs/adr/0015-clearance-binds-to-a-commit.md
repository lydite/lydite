# A clearance names one commit, and the status is consulted as well as written

`lydite review` reaches a referral but resolves nothing: a referral is
pending rather than terminal, and what ends it is a person. This records how
that person says so, and what their saying so is attached to.

A **clearance** is `/lydite clear` in a pull-request comment, from someone
with write access, and it applies to exactly one commit.

## The revision is a commit, not a tree and not a verdict

Three readings of "bound to the head SHA" were available, and they differ on
a change that alters nothing: a rebase onto a moved base.

The tree reading would survive it — the merge would produce the same tree,
which is the key `ci-orchestration.yml`'s attest job already uses. The verdict
reading would survive more: the clearance would hold for any push that did not
change what the referral said.

Both were rejected for the same reason, and it is not the size of the hole.
Each requires lydite to hold that two revisions are *the same change*, and a
clearance would then rest on that inference being right. The verdict reading
is the worse of the two, because the inference is lydite's own matching: a
change under the same uncovered paths reads as the same shape, so new code
inherits a clearance given for code nobody compared it against.

The commit reading needs no inference at all. It is also the only one a person
can hold in their head — "I cleared what I read" — and a rule the human and the
code state identically is worth more here than the rebases it costs.

## Write access is a floor, not the trust

The repository is public, so anyone may comment. Requiring push permission,
read from the hosting platform rather than from the comment, keeps a stranger
from clearing anything.

It is not the whole trust and does not pretend to be: whoever holds the
repository's credentials can post the comment, an agent included. That is the
conventional trust ADR 0013 accepts and
[#25](https://github.com/lydite/lydite/issues/25) closes with a code from an
authenticator app. The distinction worth keeping is that #25 then hardens a
boundary that exists, rather than building the first one.

Permission is evidence lydite reads *about* the commenter, so it satisfies
ADR 0014's rule that nothing an author asserts may remove a referral.

## The status is the record, so it is read before it is written

A referral publishes `pending`, an exempt or cleared change `success`, and the
isolation gate `failure`. `pending` is not a softening: a required check blocks
on anything but `success`, so it blocks exactly as hard as a failure would.
It is the accurate word, and CONTEXT.md bans the other one — a gate fails, a
referral does not. The accepted cost is that a yellow dot reads like a job
still running, which the status description answers by naming what it waits for.

Clearance therefore consults the standing status rather than recomputing the
verdict. Re-running `review` after a clearance comment could not change the
answer — a referral re-evaluated is still a referral, by definition — so the
only thing recomputation buys is telling a referral apart from the isolation
gate, and the status already carries that. Reading it means the gate is not
clearable by comment, that no pull-request content is fetched at comment time,
and that a head with no status refuses rather than clears.

That last case does most of the work against a push landing between the comment
and the job. The remainder is closed with two timestamps GitHub generates
itself: a clearance is refused when the head's status is newer than the comment,
because a person cannot have read a verdict that did not yet exist.

## Workflows drive it, and an app configures rather than decides

Producing a referral requires a diff, so the producing side must run where a
checkout exists. Keeping the deciding side beside it means one implementation
of these rules instead of one per host.

An app's job is therefore installation: making the referral context required,
which is the one field `gt` hardcodes to `ci-gate` today. Until that exists the
status is published and flipped but enforces nothing, and this is a temporary
state rather than the design —
[#34](https://github.com/lydite/lydite/issues/34) is what ends it.

`/lydite clear` and `/lydite explain` ship here.
[#33](https://github.com/lydite/lydite/issues/33) carries `/lydite exempt`,
which is held back because what it should emit is undecided rather than
unbuilt.

The comment surface is read as a webhook payload, which is the same document a
workflow writes to `GITHUB_EVENT_PATH`. That is the complete input rather than
a subset a driver chose to forward.
