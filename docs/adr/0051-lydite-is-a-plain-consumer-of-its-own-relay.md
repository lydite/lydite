# lydite is a plain consumer of `lydite/actions`, and a clearance may resolve a pending referral

lydite's own `.github/workflows/lydite-pr.yml` and `lydite-clearance.yml` mirror the shape
`lydite/actions`' reusable workflows give a consumer, deliberately not calling them — the header
of `lydite-pr.yml` says the file exists to express "the shape a consumer gets, dogfooded here".
That shape cannot present an allowlistable claim to `pr-relay`. The relay admits a gated status
by the run's verified `job_workflow_ref`
([ADR 0022](0022-a-vendor-operated-app-and-an-oidc-relay.md), amendment 2026-09-21), and an
external `lydite/actions/.github/workflows/<name>.yml@<ref>` is the only ref that can ever be
allowlisted — a local same-repo ref is refused on principle, because its ref is the pull
request's own to edit. Mirroring the shape rather than calling it means this repository's own
`referral-publish` and `clearance` jobs can never present that ref, so wiring them to the relay
as they stand means this repository's own CI fails the step, forever, correctly.

## lydite calls the reusable workflows, and pays for it like any other consumer

`lydite-pr.yml` and `lydite-clearance.yml` are rewritten to call
`lydite/actions/.github/workflows/*.yml`, the same call a consumer makes. This is a change in
kind from what the file's header currently states, and that header is rewritten with it: lydite
is the author of `lydite/actions` and also one of its consumers, and being scanned against a
pinned build rather than the pull request's own is the cost of being a real consumer rather than
a privileged one.

**What this gives up:** a change to `lydite review --publish` or the scanners is judged, on this
repository's own pull requests, against whatever `lydite/actions` has pinned — not against the
change itself — until the pin is bumped. A defect the change introduces in the publishing or
gating path is invisible here until that bump happens. This is accepted rather than worked
around; working around it is what the mirrored shape was doing, and it is exactly the shape that
cannot reach the relay.

## The pinned ref is an exact SHA, not yet `@v1`

A consumer pins `lydite/actions`' floating major tag. `@v1` exists but is deliberately held
stale until 0.2 is whole — no `lydite` release yet carries the commands `@v1`'s callers invoke,
so calling `@v1` today fails at its first command. lydite therefore pins an exact SHA, as a
stated temporary state, and switches to `@v1` once it is usable
([lydite/lydite#216](https://github.com/lydite/lydite/issues/216)). Every other repository this
ADR's reasoning would apply to pins the tag from the start; lydite does not, only because no
tag exists yet that would work.

## The reusable workflows split so the relay can tell a job apart by file

`lydite/actions`' existing `lydite.yml` runs `referral`, `test` and `publish` as jobs of one
reusable workflow, so they share one `job_workflow_ref` — a claim the relay cannot use to admit
a status-writing job while refusing a job that runs the pull request's own code, because both
would present the same ref. Two new reusable workflow files carry the isolated jobs instead:
`lydite-referral-publish.yml` (base-build, `statuses: write`, no code from the pull request's own
tree) and `lydite-clearance.yml` (`issue_comment`, `statuses: write` and `pull-requests: write`,
runs from the default branch only). Each gets its own allowlist entry
(`REFERRAL_WORKFLOW_REFS`, `CLEARANCE_WORKFLOW_REFS`), and a consumer wanting the identity
features adds two more `uses:` lines beside its call to `lydite.yml`, one per new file, each
pinned to the ref the consumer chooses.

Nesting the call inside `lydite.yml` instead — so a consumer keeps one call site — was
considered and rejected: a nested `uses:` needs a literal ref, which a workflow file cannot
supply as its own commit SHA, so the nested call would necessarily float to a different version
than the one a SHA-pinning consumer chose for everything else. A second, separate `uses:` line
is more to copy, but every ref on it is one the consumer actually pinned.

## `lydite/referral` stays the one required, gating context

The alternative — moving the required check to `lydite/clearance`, since that is the only
context a clearance ref may post on the relay route — was proposed during this design pass and
rejected. A consumer's requirement is non-negotiable: a non-green `lydite/referral` blocks
merge, and a `pending` one needs a person to clear it before it does, exactly what `failure`
(the isolation gate, no comment resolves it), `pending` (standing referral) and `success`
(exempt, clean or cleared) already encode in `internal/clearance/decide.go`. Moving the
required check to `lydite/clearance` — a context that only ever records who cleared, never
gates on its own — would have handed consumers a check that no longer expresses "non-green
blocks, yellow needs a person", which is the property they require. `lydite/clearance` remains
the record; `lydite/referral` remains the one status a ruleset requires, on this repository and
on every consumer.

## A clearance ref may resolve a pending referral to success

This narrows ADR 0022's "one ref is trusted for exactly one context; a ref in both lists is
refused" — the invariant this design pass's handoff listed as settled and not to be reopened.
It is reopened because the alternative is the required check no longer being satisfiable from
the relay route at all: a clearance ref may post `lydite/clearance` only, `lydite/referral`
never, so `/lydite clear` on a relay consumer would record a clearance nothing acts on, and a
consumer requiring `lydite/referral` would stay blocked after every clearance, forever.

The exception is narrow and one-directional. A run presenting an allowlisted
`CLEARANCE_WORKFLOW_REFS` ref, `event_name: issue_comment` and a `refs/heads/` ref — the three
claims clearance authority already requires — may additionally post `state: success` to
`lydite/referral`, and only when the relay's own live read of the status standing on that head
(resolved with the installation token, never taken from the request body) is `pending`. A
`failure` or `error` standing status is never touched by this path: the isolation gate stays
unclearable by any comment, on the relay route exactly as it is on the direct-post route today.
`lydite/clearance` is posted first, so a partial failure never leaves a green referral with no
clearance record — the same ordering ADR 0022's 2026-09-21 note already describes for the
direct-post route.

**What this gives up:** the relay's trust model is no longer "one ref, one context, full stop".
It is "one ref, one context it may post outright, plus a second context it may move only one
way, from a state naming no verdict of its own to the verdict another ref already reached". A
compromised clearance-allowlisted ref could clear a pending referral without a person's
comment ever having been read correctly by `clearance.Decide` upstream of this — the same
exposure the direct-post route already carries, since it resolves `lydite/referral` on the same
trust. Nothing here widens what a compromised clearance ref could do beyond what it can do
today; it only lets the relay route do it too.

## What is unchanged

Everything else ADR 0022 and its amendments record: the two-App split, the four OIDC claim
checks, per-Worker secrets, the `409`/`403`/`5xx` status ladder and its deliberate asymmetry
with the comment ladder, the relay holding no state, and the allowlist as two Wrangler vars of
exact strings with no patterns. `pr-relay`'s per-`job_workflow_ref` scoping to one context is
unchanged for every context except this one exception.
