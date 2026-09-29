# The merge-queue submission is a Flow over a relay client

[#278](https://github.com/lydite/lydite/issues/278) asked the queue path onto the same migration
`publish` took, and offered it the same framing the publish issue used: a `RelaySink` beside the
pilot's `SCMSink`, one more transport-shaped node the engine would need to know about.
`internal/flows/queue` (package `queueflow`) and `internal/stages/queue` (package `queuestages`)
put `lydite clearance queue` on the scaffold instead, over a new domain package, `internal/relay`.
This records why, in the terms ADR 0074 already settled for `publish` and does not repeat, and
what is different here: queue is the one flow on the scaffold that writes to the platform — through
the relay — while holding no credential of its own. `clearance` and `review` are the flows that
write with a token, and each establishes trust and the repository first, before it touches its
payload.

## Five stages, in a straight line

`queueflow.New` declares `load-event`, `resolve-base`, `recompute-decision`, `mint-token` and
`submit-comparison`, none conditioned on another's output. `load-event` reads the entry the
`merge_group` payload names; `resolve-base` resolves the commit the decision is recomputed
against; `recompute-decision` recomputes and fingerprints that decision; `mint-token` mints the
OIDC token the relay accepts, audienced to the relay's own origin; `submit-comparison` submits the
fingerprint for the relay to compare and publish. `mint-token` is declared after
`recompute-decision`: nothing about minting depends on the decision, but a run that cannot resolve
the base or recompute the decision has nothing to submit, and should fail for that reason before it
fails for a missing mint endpoint.

This is worth saying plainly rather than working around: the queue path has no branch and no
policy beyond the default `FailFlow`, so it needs none of Flow's conditional machinery. It is on
Flow for the same reason `publish` is — structural consistency with the commands already built on
the scaffold — not because five straight stages needed an engine to run them in order.

## Not a `RelaySink`, and not an `SCMRepository`

No `Sink` type exists anywhere in `internal/flow` or the clearance pilot, as ADR 0074 already
recorded for `publish`'s proposed `Source`; the same is true here for `RelaySink`. A stage that
submits a comparison to the relay is `SubmitComparison`, one stage among five, exactly the shape
`WriteComment` already is for `publish`.

Nor is the relay reached through `forge.SCMRepository`, the interface every platform-writing stage
in the clearance and review flows goes through. `scmstages.InitSCM` builds that interface from a
`trust.TrustedContext`'s token and repository — both of which this job is built to not hold: the
relay resolves the repository from the verified OIDC claim, not from a token this process presents
for writing, and there is no writing token here for `SCMRepository` to wrap. Widening that
interface to cover a client with no token and no repository of its own would change its shape for
every other caller to fit a job that was designed, deliberately, to hold neither.

## `internal/relay` is its own domain package

`internal/relay` composes the two requests the queue path makes — minting the token, submitting
the comparison — and reads the answers back. It imports nothing above it: no `cmd/lydite`, no
`internal/ui`, no `internal/flow`. `queuestages.MintToken` and `queuestages.SubmitComparison` are
its only callers, each a thin stage that turns a typed `In` into a call and its result into a
typed `Out` — the same relationship `clearancestages.RenderStatuses` has to `internal/clearance`,
domain logic underneath a stage rather than inside it.

## The CLI reads the mint environment; no stage names an environment variable

`runQueue` reads `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` and passes
both through as `queueflow.Params` fields, empty or not. `relay.MintIDToken` answers
`ErrNoMintEndpoint` when either is empty, at `mint-token`'s own point in the five-stage sequence —
after the payload, the base and the decision have each had their chance to fail first. Reading
both up front in the CLI and failing before the flow even runs was rejected: it would reorder the
errors a run with two problems reports, surfacing "no mint endpoint" ahead of a payload or a base
that is *also* wrong, when the more useful first answer is whichever failure sits earliest in the
work the flow actually has to do. `truststages.InitTrust` stays the one stage in this codebase that
reads the process environment; the CLI reading these two variables for itself, rather than adding
a second stage that reads the environment, keeps that true.

## No `init-trust`, no `init-scm`

`clearance` and `review` each declare `init-trust` and `init-scm` before either touches its
payload, because each of them writes to the platform with a token `trust.FromEnvironment()`
resolves. `queueflow` declares neither. The job this flow runs in is built to hold no writing
credential: `runQueue` refuses outright when `--relay` is empty, precisely because there is no
fallback to a token this job does not have. Adding `init-trust` here would require
`GITHUB_REPOSITORY` for a run that uses no `SCMRepository` and holds no token to build one from —
a hard dependency added for a value nothing downstream reads.

That absence is also why `load-event` reads the `merge_group` payload's `head_sha`, `head_ref` and
`base_ref` as data, rather than a pointer this flow then resolves live — the choice
[ADR 0061](0061-trust-and-the-repository-come-first-and-a-webhook-payload-only-points.md) makes
for the clearance pilot's comment id, and the general rule
[architecture.md](../../agentic/references/architecture.md) states as "trust and the repository
come first." That rule holds where a trusted credential exists to resolve something live against:
the pilot's `forge.ReadCommentRef` reads only an id and a repository claim off the payload, and
every field the command actually acts on — a comment's body, its author, its pull request — is
then fetched live through `forge.SCMRepository`, because a credential-holding job can, and because
a payload's copy of any of those can be stale by the time the job runs. This job holds no
credential to fetch anything live with. The relay is what resolves the pull request and the head
live, from the verified claim, and compares what this job submits against what it reads there
itself — the live resolution ADR 0061 describes still happens, just on the other side of the wire,
where the credential actually is. Reading the payload's own fields is therefore not a narrower
version of trusting the payload for its content; it is the whole of what this job has to offer as
its own account of what it computed, for the relay to check.

This is also why `load-event` is a stage here and not a CLI-side pointer read ahead of the flow, as
the pilot's `forge.ReadCommentRef` is. There, the payload names only what the flow then reads live
with a credential — a pointer, consumed once and never carried further. Here the payload's fields
*are* the data the recomputed decision and the submission are built from: `HeadSHA` is the revision
a verdict is published against, `HeadRef` and `PullRequest` are what the relay checks the request
against, `BaseBranch` is what `resolve-base` resolves the merge-base with. Reading them is part of
the flow's own work, not a preamble to it, so it is a stage other stages bind to by name
(`flow.FromStage(StageLoadEvent, ...)`) like any other.

## Rejected

**A `RelaySink`.** No `Sink` concept exists in `internal/flow`, as ADR 0074 already settled for
`publish`'s proposed `Source`. Building one for this migration alone would give one flow a shape no
other flow shares, for a distinction — "this stage talks to the relay" — the engine has never
needed to draw to run a stage.

**An `SCMRepository` implementation backed by the relay.** Considered as a way to keep every
platform-writing stage behind one interface. Rejected because `SCMRepository` is built from a
`TrustedContext`'s token and repository, and this job is built to hold neither — the relay derives
the repository from the OIDC claim, not from anything this process asserts. Widening the interface
to admit a caller with no token and no repository would weaken what it means for every other
implementation.

**Leaving `queue` a hand-documented exception, off Flow.** The command has no branching either way,
which is a real argument for staying a plain `RunE` body — `publish` made almost this same
argument for itself and was put on the scaffold anyway, for consistency rather than necessity. The
same reasoning applies here: a command with nothing to condition is exactly the case that shows
adopting the scaffold costs nothing extra.

**A second stage that reads the environment.** Mirroring `init-trust`'s shape with a stage that
reads `ACTIONS_ID_TOKEN_REQUEST_URL`/`_TOKEN` was considered, so nothing in `cmd/lydite` would read
the environment for this path. Rejected because it duplicates a role `truststages.InitTrust`
already fills for the one thing this run actually needs sealed — a credential, and this run holds
none — for a pair of values that carry no comparable weight: unlike `GITHUB_TOKEN`, the mint
request token is meaningless without also holding the request URL and the audience, so nothing is
gained by sealing it the way a real credential is sealed. Reading it in the CLI and passing it
through as fields keeps `queuestages` a pure function of its own `In`, same as every other stage.

**An `init-trust`/`init-scm` prefix, with the payload read once as a CLI-side pointer.** Modelled
directly on the clearance pilot: resolve trust and a repository first, read the payload's id-shaped
fields as a pointer, and let a later stage fetch the rest live. Rejected because there is nothing
live to fetch here — this job holds no credential to fetch with, and the relay is what performs the
live resolution ADR 0061 asks for, on data this job submits rather than data this job reads back.
Building the prefix anyway would model a credential and a repository this run does not have, to
guard a fetch this run never makes.

## Consequences

- `internal/relay`'s own discipline governs every answer: a non-200 from either endpoint is an
  error with no fallback, and a 200 naming no state is refused rather than read as success — there
  is no substitute route here the way ADR 0037 sorts a relay's failure into three for a caller that
  does have one, because this job holds no token that could publish anything if the relay does not.
- `relay.MergeGroupRequest` never carries a repository. The relay takes it from the verified claim,
  so there is nothing in the body that could name another one, and nothing for `load-event` to read
  a repository off the payload for.
- The token `mint-token` produces is audienced to the relay's own origin and to nothing else, so a
  token minted for this run cannot be replayed against another service even if it were captured.
- A future command with this same shape — no writing credential, a verified claim resolving
  everything live on the other side of a relay call — reads its own payload as data the same way,
  rather than reaching for `init-trust`/`init-scm` out of habit.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md)'s "The queue
flow" section for the five stages among the four layers, and
[ADR 0074](0074-publish-runs-as-a-flow-of-ordinary-stages.md) for the Source/Sink question this
record does not repeat.
