# A deterministic relay misconfiguration fails the step, not the fallback

[ADR 0022](0022-a-vendor-operated-app-and-an-oidc-relay.md) gave lydite an identity —
a comment and a review that arrive as `lydite` rather than as the consumer's own
`github-actions[bot]` — and made the `github-token` path a required fallback rather
than a stopgap, so that the App stays genuinely optional and the relay's absence is
never an outage. The composite actions implement that by treating the relay's answer
as a single bit: `200`, or fall back.

That bit is too narrow. It puts a token audience the consumer typed with a trailing
slash, an operations document whose version the relay will not apply, and a run whose
`ref` is not a pull request's into the same bucket as "the App is not installed here" —
and the visible outcome of all of them is a green job with a comment on the pull
request. Nothing distinguishes the relay working from the relay having been configured
wrongly, on that run or on any run after it, because none of those answers change on a
retry. The consumer who cared enough to configure the relay is the one the silence is
worst for: they get exactly the surface they would have got by configuring nothing.

**The relay's answer sorts into three buckets. One falls back silently, one falls back
under a `::warning::`, and one fails the step with no fallback at all.**

## What is an answer, what is an outage, and what is a mistake

The three buckets are not severities. They are three different statements about whether
the next run will answer the same, and whether anything the consumer could do would
change it.

**Falls back, silently: no relay configured, and `409`.** These are the design working.
An unset `LYDITE_RELAY_URL` skips the relay step entirely. A `409` is the relay saying
the lydite App is not installed on this repository, which ADR 0022 already insists is an
*answer* and not an error — it is the ordinary state of every repository that has not
opted in, and annotating it would make not opting in look like something going wrong.

**Falls back, under a `::warning::`: `5xx`, and curl's `000`.** A `502` is any GitHub or
JWT error the relay met while minting an installation token or writing; `000` is the
relay not being reachable at all. Both are conditions the consumer did not cause and
cannot fix, and both plausibly resolve on the next run. Failing a consumer's pull request
for a lydite outage is the thing the fallback exists to prevent. Passing it unremarked is
how an outage lasts a week, so the fallback still happens and the annotation is what says
it did.

**Fails the step: `401`, `404`, `400`, and — on `/comment` — `403`.** A `401` is no bearer
token presented, or an OIDC token whose signature, issuer, audience or expiry the relay
would not accept — a genuinely wrong `aud`, one built from a `LYDITE_RELAY_URL` naming the
wrong scheme, host, or port, since the relay compares `aud` by exact string. A `404` is the
request path itself malformed before any token is checked — the case a consumer reaches by
writing the origin with a trailing slash, since both composite actions build the request as
`${RELAY}/comment` (or `/review`) and a trailing slash turns that into a double slash the
relay's router does not recognize as either route. A `400` on `/comment` is a payload
missing its `marker` or `body`; a `400` on `/review` is an operations document whose
`version` this relay does not apply, which is a CLI-and-relay skew. A `403` on `/comment`
is a run whose `ref` is not a pull-request ref, or a submitted `pull_request` number that
does not match the one the relay derived from `ref`.

Every one of those is deterministic. The same run, retried, answers the same, and no
amount of waiting fixes any of them — what fixes them is an edit to a variable, a
version, or a workflow. A fallback here is not resilience; it is a permanent substitution
of one identity for another, performed silently, in exactly the repository that asked for
the other one.

## The two endpoints disagree about `403`, because their `403`s differ

`/review` returns `403` for a third reason `/comment` has not: an operation naming a
review-comment id that is not currently on the pull request. The relay re-reads those ids
with the App's own token at apply time, and refuses the whole document if any `reply` or
`delete` names one outside that set. A thread deleted between `lydite threads` computing
the delta and the relay applying it produces exactly that — a live race, on a correctly
configured relay, which the fallback's recomputed delta settles on its own.

ADR 0022 fixed that nothing at the action layer can tell those apart: *a rejection carries
no detail*, because a verifier that says which check failed tells an attacker how to get
one step closer. That property is worth more than this classification is, so the
classification bends around it rather than asking the relay to explain itself.

So `/review`'s `403` falls back with a warning, and `/comment`'s fails. This is a stated
compromise, not an oversight. On `/comment` the ambiguity does not exist — the comment-id
check lives only on `/review` — so a `403` there is unambiguously the ref or the number,
and failing on it costs nothing. On `/review` the ambiguity does exist, and it is resolved
toward the fallback: a misconfigured `/review` loses its loud failure, and a race loses its
ability to fail a job. The misconfigurations that reach `/review` reach `/comment` too, in
the same job, moments earlier — a wrong audience or a non-pull-request `ref` fails there
first — so the loud failure is not actually lost, only relocated. The one misconfiguration
that `/review` holds alone is the ops-version skew, and that answers `400`.

## What this does not touch

A fork's pull request cannot mint an Actions OIDC token. That fails at the `curl -fsS`
mint, under `set -e`, before any request reaches the relay and before a status exists to
classify. This decision changes nothing about that path, and does not make it a fourth
bucket.

Neither the relay nor `lydite publish` changes. The classification is entirely in the two
composite actions, which is where the choice between the two transports already lived.

## Consequences

- A consumer whose `LYDITE_RELAY_URL` is wrong gets a red `publish` job naming the status,
  on the first pull request after they set it, rather than a green one under the wrong
  byline forever.
- The failing branch sets no `posted`/`applied` output. The step's own non-zero exit is the
  whole of what it says: a failing step in a composite action fails the composite, which
  fails the calling job, and the fallback step never runs.
- A `::warning::` on a `5xx` puts a lydite outage on the consumer's own run summary, which
  is the only channel lydite has to them.
- The same classification has to land in `lydite/actions`, whose composites carry the copy
  consumers actually run. Nothing enforces that; it is tracked there.
- A status the relay does not currently return is failed rather than fallen back on. A
  fourth behaviour arriving unclassified is a thing to notice, not a thing to absorb.
