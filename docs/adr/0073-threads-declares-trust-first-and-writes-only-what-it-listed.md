# `threads` declares trust first, and writes only what it listed itself

`threadsflow.New` declares `init-trust` and `init-scm` before every other stage, unconditionally
— the opposite of `review`'s ordering ([ADR 0071](0071-a-review-reads-its-pull-request-from-the-payload-and-posts-to-the-revision-it-measured.md)).
The rule the two commands share is the same one: a flow declares trust where it is first needed.
`review` needs no credential until it posts, so it declares trust late and only under the
condition that guards posting. `threads` needs one from its very first read — even a run given
`--ops` with no `--apply` lists the threads standing on the pull request live, because the plan
it writes is a delta against them, not against nothing. There is no earlier point in the flow
where that need does not already hold, so trust is declared first, the way clearance declares it
first for its own reason ([ADR 0061](0061-trust-and-the-repository-come-first-and-a-webhook-payload-only-points.md)).
This record is the "threads" half of that comparison, and two further decisions the same
refactor settled: how a stage that writes more than once reports what landed before it failed,
and why take-down and answer write only to ids this run's own listing returned.

## Trust first, and the failure precedence that falls out of it

Declaring `init-trust`, `init-scm` and `load-pull-request` first, in that order, ahead of
`read-findings` and everything that reconciles, fixes the order a misconfigured run fails in:
repository (`init-trust`) → token (`init-scm`'s `scmstages.ErrNoCredential`) → event
(`load-pull-request`) → everything else. A repository trust refuses is reported in `internal/trust`'s
own words — the one place this command's wording differs from what it read before moving onto a
flow, and only in a misconfigured job, because `trust.FromEnvironment` checks the repository ahead
of the token. `trust` returns untyped errors, so restoring the old wording would mean
string-matching another package's prose, which the CLI does not do for any other stage's failure
either.

### Rejected: keeping the old wording by matching trust's message text

A repository failure could be caught and re-worded to match what the pre-flow command printed.
Rejected for the reason [ADR 0071](0071-a-review-reads-its-pull-request-from-the-payload-and-posts-to-the-revision-it-measured.md)
gives for the same choice on `review`: `trust`'s errors are untyped, and its prose is that
package's own to change, so telling a missing repository from a malformed one apart, to reproduce
the old sentence exactly, would mean matching a message that package is free to change on its own
schedule. A string match is a coupling to another package's prose the CLI does not accept anywhere
else, and the one deviation this causes — repository checked ahead of token — is only visible in a
misconfigured job, not in what a working run reports.

## A stage that performs a sequence of writes reports what landed in its error

A failed stage's `Out` is unavailable to its caller — `flow.Output` returns nothing for a stage
that did not succeed — so a stage whose job is a sequence of writes has to carry whatever
progress it made before failing somewhere else: its error. Two stages in this flow write more
than once per call, and both wrap their error in a typed value implementing `Unwrap`, so the
underlying cause is still reachable by `errors.Is`/`errors.As` while the progress rides alongside
it:

- `TakeDown` deletes every comment `Ops.Delete` names, and a delete the platform refuses is
  answered instead rather than left standing — not a failure of the stage, one of its two ordinary
  outcomes. A `*TakeDownError{Answered []int64, Err error}` carries every comment answered before
  a real failure, so the CLI still prints a warning for each refusal answered on the way to the
  one that stopped the run — the same warning it prints for a successful run's refusals, read off
  `TakeDownOut.Answered` instead.
- `Open` posts one review for every line-anchored claim and one call per file-anchored claim, and
  either can be refused after some of the other has already landed. A `*OpenError{Lost, Posted
  int, Err error}` names both counts; its `Error()` is `"<Lost> located finding(s) reached no
  surface: <Err>"`, because the number a reader of a job log needs first is how many claims never
  reached the pull request, not how many did.

### Rejected: a failed stage returns its partial output alongside the error

`flow.Output` could instead read the last value a stage returned even when it also returned an
error, so `TakeDownOut` or `OpenOut` would still be readable after a failure. Rejected: that
widens what every stage in the engine is allowed to return, for a shape only these two stages
need, and it blurs the flow's own contract that a failed stage's output does not exist — nothing
downstream should have to ask whether a `flow.Output` value it reads came from a stage that
actually succeeded. Carrying the partial progress in the error keeps that contract intact and
costs nothing but an `Unwrap` on two types.

## Write targets come from this run's own listing, never a second live read

`take-down`, `answer` and `open` write only to ids `list-threads` returned earlier in the same
run — `Plan` computes `Ops` from that listing, and the write stages that follow read `Ops`, never
the platform again, before deleting or replying. No second listing is made before applying. That
is deliberate, and different from the relay: `source/cloud-services/pr-relay`'s `/review` re-lists
the pull request's review comments itself, immediately before applying, and refuses the whole
request if any `reply` or `delete` in the caller's document names an id outside that fresh
listing — [the rule on resolving a write's target live](../../agentic/rules/resolve-a-writes-target-live-never-trust-the-claim-or-the-body.md)
is why: the relay's ops document arrives from a caller over HTTP, an unverified claim about what
should be written, and the only thing the relay can trust unconditionally is what it lists for
itself in the same call. `threads` is not that shape — its ids are its own, read by its own
credential in the same run that then writes them, never a claim carried in from outside the
process — so a second live listing before applying would check a list against itself and buy
nothing a caller of the relay's endpoint actually needs checked here.

### Rejected: listing the threads again immediately before applying

Re-listing right before `take-down`, to mirror the relay's re-list, was considered and rejected.
The relay's re-list defends against a caller's claim going stale between when it was made and
when the relay acts on it — two different actors, separated by a network call. `threads` computes
its plan and applies it in the same process, off the same listing, with no caller in between to
have made a claim at all; the ids `take-down` and `open` write to are read out of `Ops`, which was
computed from `list-threads`'s own answer earlier in this same run. A second listing would only
be able to disagree with the first because something else wrote to the pull request in between —
a race no re-list before applying closes, since the same race can just as well land between the
re-list and the write that follows it. The relay's re-list is a different remedy for a different
threat: an untrusted caller's assertion, not a live value moving underneath a trusted run.

## Context

Two further decisions this refactor makes, recorded here because both bear on the same seam:

**`SCMRepository` widens rather than splitting.** The five review-thread calls
(`ReviewComments`, `CreateReview`, `CreateFileComment`, `ReplyToReviewComment`,
`DeleteReviewComment`) join `forge.SCMRepository` instead of a separate `ReviewThreads`
interface. [ADR 0060](0060-stages-read-and-write-the-platform-through-an-scmrepository-interface-ahead-of-a-second-vendor.md)
already settled that the platform is reached through one interface, built once from a
`trust.TrustedContext`, and named review threads as absent from it for exactly one reason: no
stage reached for them yet. Widening the interface, that record says, is "a deliberate change made
when a new stage needs a new call" — precisely what the threads stages are: the new stages
reaching for `ReviewComments`, `CreateReview`, `CreateFileComment`, `ReplyToReviewComment` and
`DeleteReviewComment` for the first time, which is the condition ADR 0060 set for widening rather
than an exception to it. A second interface on this occasion would reverse that decision instead
of following it.

**The stages take the pull request as one bound field, not its parts.** `threadsstages.ListThreads`,
`Plan`, `TakeDown`, `Answer` and `Open` all take `Ref forge.PullRequestRef`, bound once to
`load-pull-request`'s own `Ref` output, rather than separate `Number`/`SHA` fields each bound to
one field of it. The flow engine's builder binds one whole exported field per `With` call and has
no notion of a path into a nested one, so a stage that wanted only `Ref.Number` would still need
the flow to bind the whole struct and the stage to read one field off it — which is what every
one of these stages already does, the same shape `reviewstages.ComposeStatus` takes its own
`Ref forge.PullRequestRef` in.

**The old per-command platform plumbing is removed, not kept as a shim.** `resolveTarget`,
`resolvePullRequest`, `publishTarget` and `pullRequestRef` in `cmd/lydite/status.go` — the
credential and target resolution `threads` shared with the other commands that used to read the
environment for themselves — are deleted rather than left standing beside the flow that replaces
them. Keeping both would mean two places read a credential for the same command — the flow's
`truststages.InitTrust`, sealed into a `trust.TrustedContext` nothing outside `internal/trust` can
read or forge, and whatever the old plumbing still called `os.Getenv` for — and the whole point of
`TrustedContext` is that a run has exactly one.

## Consequences

- A `threads` run given `--ops` with no `--apply` still needs `GITHUB_REPOSITORY` and a token: it
  lists the pull request's standing threads live to compute its plan, whether or not it goes on to
  apply that plan.
- The CLI reads the repository slug for its own rows off `init-trust`'s `Trusted.Repository()`,
  never off the payload or a flag — see [`agentic/references/architecture.md`](../../agentic/references/architecture.md)'s
  "The threads flow" section for the stage table this failure precedence and these typed errors
  read against.
- A comment `take-down` or `open` fails to write to is always one this same run found on the pull
  request first; nothing is ever written to an id supplied any other way.
