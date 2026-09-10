# A located finding is a review thread, computed by the CLI and applied by two transports

[ADR 0030](0030-findings-are-data-in-the-report-document.md) made a finding data:
a located claim about the code that one edit clears, carrying a content-derived
fingerprint and an anchor saying how precisely it reaches the change. Four
producers emit them. Nothing consumed the anchor.

The one surface lydite had is
[ADR 0023](0023-one-standing-comment-rendered-by-the-cli.md)'s standing comment,
and it cannot do what a located claim needs. It cannot be resolved per finding,
it cannot sit on a line, and it keeps no record of any one claim across pushes —
it is replaced whole on every run. A reviewer reading "internal/runner/runner.go:453
is covered by no test" in a collapsed section has to go and find line 453; the
platform already has a place for a remark about line 453, and it is the one place
an author cannot merge past without answering.

**A finding that reaches the change becomes a review thread on the line or the
file it is about. The standing comment keeps the findings that reach it nowhere,
and no finding appears in both. `lydite threads` computes the delta between the
threads already standing and this run's claims and writes it as an operations
document; two transports apply that identical document — the relay under
lydite's own identity, and the CLI's own `--apply` under the workflow's token.**

## The partition needs no coordination

The rule that separates the two surfaces is one sentence: *the comment carries
what is true of the change; the review carries what is true of a line.* It is
implemented as a partition on `Anchor`, which ADR 0030 already put in the
document and which the gate that made the claim already decided. The comment
takes `AnchorNowhere`; the review takes the rest. Neither renderer has to know
what the other did.

That anchor is decided against `coverage.ChangedLines`, which is `--unified=0` —
the change's added lines with no context. A hosting platform's diff hunks carry
three lines of context around each of those, and the API refuses a comment
outside them with `422 line must be part of the diff` even where the web UI
would let a human comment. lydite's set is therefore a strict subset: *"lydite
says anchorable" implies "the platform accepts it"*, never the reverse.

**The narrowing is unconditional.** `lydite publish` drops located findings
whether or not any thread is being posted, because a developer running it
locally has to read exactly the comment a reviewer sees. That is ADR 0023's
parity property, and a comment that rendered differently depending on whether a
hosting platform was reachable would lose it.

The cost is that a failing row's asides — mutation's `3 did not compile` — leave
the comment along with the rest of that row's `Detail`, since a row renders its
findings or its detail and not both. They stay on the terminal and in the log
the row names.

## This amends ADR 0030 on the row link, and does not undo it

ADR 0030 removed the link from a finding back to its row, on the grounds that
*"the grouping buys nothing because a finding carries its own gate and component
regardless"*. It buys something now: without it, the comment cannot tell which
of a failing row's claims it is keeping and which it is leaving to a thread, and
would either repeat every located claim or lose every unanchored one.

So `finding.Finding` gains `Row`, the label of the row that reported it, written
by the producer that already holds both. That is not the thing ADR 0030 refused.
It refused two specific shapes: nesting findings inside `ui.Row`, which would
give the rendering type a field it never renders, and parsing `gosec(cli)` back
apart to recover a component. Neither happens here — a finding still carries its
own gate and component, and the label is carried rather than parsed.

## The delta is the CLI's, and the transports decide nothing

Matching a fingerprint to a thread, deciding whether lydite may delete one, and
composing what a thread says are lydite's vocabulary. A second implementation of
them in a Worker is a copy one release behind forever, which is ADR 0023's own
argument for rendering the comment in the CLI.

So `lydite threads` reads `--reports`, fetches the threads standing on the pull
request, computes the delta, and **always** writes it to `--ops <file>`. It
applies it only under `--apply`. The relay's `POST /review` applies the same
document. One delta, two dumb transports.

**Reading the prior state uses the job's own token on both paths**, and that is
deliberate rather than an oversight against
[ADR 0022](0022-a-vendor-operated-app-and-an-oidc-relay.md). The delta has to be
computed somewhere that can read the pull request, and the publish job already
holds `pull-requests: write` for the comment's `github-token` fallback — so this
costs no credential that was not already there. What the relay takes over is the
*write*, which is the half ADR 0022 is about: the job that runs a pull request's
own code holds nothing that can write anywhere else.

`lydite publish` stays pure. No network, no token, no knowledge of a hosting
platform: a new command exists rather than a flag on that one for exactly that
reason.

**The operations document is lydite's own shape and a private versioned wire.**
It goes to `--ops <file>` and not into `.lydite-reports/`: nothing about it is
promised to a consumer, and `lydite test plan` is the precedent for a command
that reaches no verdict writing no report document. A reader that does not
recognise the version refuses the whole document rather than applying the half
it understands.

A `delete` operation carries the body to post if the platform refuses it. The
shape sketched during design was a bare comment id, and it cannot be: the
refusal path replies, and a transport composing its own prose is a second place
lydite's words live — the thing this decision exists to prevent.

## One predicate governs both branches of a thread's life

**lydite is the only participant**, read off the marker in every comment of the
thread and never off an author. The author is whoever's token posted it, and
ADR 0022 makes that change in both directions.

| Situation | lydite is alone | Somebody else spoke |
|---|---|---|
| the claim is gone | delete the thread | reply, and leave it standing |
| the thread is outdated, the claim persists | delete and reopen at the current line | reply saying where it is, and leave it standing |

A delete the platform refuses takes the same path as "somebody else spoke",
which is why the operation carries a body.

**An outdated thread is moved rather than left**, and the reason is not tidiness.
GitHub collapses an outdated thread behind its "Show outdated" toggle, so a
thread that blocks the merge becomes a thread the author cannot see. That is
worse than the notification a repost costs. `position` comes back null while
`original_line` still holds, which is how it is detected.

A fixed claim leaves no per-finding record. That is accepted:
[ADR 0009](0009-quality-history-storage-and-access.md)'s per-finding history is a
later, additive step, and the fingerprint is what makes it possible.

## The marker is the fingerprint

`<!-- lydite:finding:<fingerprint> -->`, where the token after the prefix *is*
the fingerprint, version prefix included. No second identifier is derived: two
ids for one claim is two things to keep in step, and the one nobody reads is the
one that drifts.

The parser accepts any token and returns it verbatim, including one from a
formula this binary has never emitted. A thread written under an older formula
therefore matches no current finding, takes the cleared path, and the claim is
posted afresh under the new one. That is what makes a fingerprint-formula bump
self-heal in one round of delete-and-repost, with no migration code and nothing
to recognise a version by.

Matching on the marker and never on the author, for `ui.Marker`'s reason.

## What the relay checks, and why the whole request

`POST /review` keeps every property `/comment` holds: RS256 fixed rather than
read from the token, a rejection carrying no detail, nothing stored, the PKCS#8
key, and "the app is not installed" as an *answer* that sends the client to its
own token.

It adds one. Before applying anything it lists the pull request's own review
comments and **refuses the whole request if any `reply` or `delete` names an id
outside that set**. A comment id is a number the caller supplies, and `ref` is
the only thing a run cannot choose; without the check, a run could delete a
comment on any pull request in the repository. Refusing the whole document
rather than the one operation is what stops the answer being usable to probe
which ids exist.

It answers with an outcome per operation, so a posted review and a refused
delete are distinguishable.

## A review that cannot be posted fails the job

Never a silent green. The run says how many located findings reached no surface,
and fails; the findings are still in the terminal, in the job log and in the
uploaded `.lydite-reports/` artifact. The memory store already records
`relay-audience-mismatch-degrades-silently` as a standing hazard of this design,
and a review that quietly posted nothing would be a second instance of it.

## What lydite cannot do, and the corner case it does not engineer around

**lydite can never resolve a thread.** GraphQL `resolveReviewThread` requires
`Contents: write` rather than `pull_requests: write`, and there is no REST
alternative — confirmed in two independent GitHub community discussions
([44650](https://github.com/orgs/community/discussions/44650),
[204269](https://github.com/orgs/community/discussions/204269)). Widening the
relay to `contents: write` is exactly what ADR 0022's two-App split forbids, so
this is blocked by the same wall as
[#49](https://github.com/lydite/lydite/issues/49). Every other call it needs is
covered by `pull_requests: write`.

**A consumer that installs the app mid-life has threads neither identity can
delete.** An identity may only delete comments it authored, so the ones
`github-actions[bot]` wrote stay standing under the app, and the refusal takes
the "somebody else spoke" path: they get a reply saying the claim is cleared,
and a person closes them. The expectation is that a consumer installs the app
from the start or never, and this is a corner case to state rather than a
migration to build — no re-authoring pass, and no second marker scheme.

## Threads block, deliberately

`require_thread_resolution` makes an unresolved thread block a merge, which is
lydite's first real merge gate. A thread is a **soft gate**: blocking, but
clearable by any writer without touching the code, which is what makes a false
positive survivable. That partly answers
[#75](https://github.com/lydite/lydite/issues/75) by a different door than the
required-check route; #75 stays open for the rest.

**Uncapped.** Every located finding gets a thread. They self-clean under the
lifecycle above, so N threads is a worklist that empties itself rather than N
items of debt. The only bound is mechanical: one review per run.

**The terminal is unchanged.** There is no thread locally, so `lydite scan` and
`lydite mutation` keep printing everything they always did.

## The costs, stated

`internal/forge` grows from six calls to ten — list review comments, create a
review, reply to a comment, delete a comment. Its stance is that *a client
covering six calls is cheaper to audit than one covering the platform*, and four
more is a real widening. The argument survives at the new size: ten named calls
still fit on one screen and every one is reachable from a line of lydite's own.
It is stated rather than quietly deleted.

The publish job now makes one more round trip on the ordinary path, and two when
the relay is tried and falls back: the delta is recomputed rather than the
document replayed, because threads may have moved while the relay was being
tried and a stale document would answer a thread that is no longer there.

`lydite threads` dedups by fingerprint on read, first occurrence wins, and names
the drop on stderr. It consumes documents a local run wrote with no fold at all,
so it cannot rest on the fold being fixed — the fold's own duplication is
[#123](https://github.com/lydite/lydite/issues/123) and is deliberately not
fixed here.
