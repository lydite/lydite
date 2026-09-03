# lydite posts as its own App, and CI authenticates with no credential at all

The referral comment has always been posted by `github-actions` with the
workflow's own `GITHUB_TOKEN`. That was a default rather than a decision, and it
is indistinguishable from any other workflow in the repository: nothing on the
comment says lydite wrote it, and nothing bounds what the token that wrote it
could have done instead.

**lydite operates two GitHub Apps. `lydite` holds `pull-requests: write` and
`statuses: write` and is the visible identity on every comment and status;
`lydite-dashboard` holds `contents: read` only, is opt-in, and exists for
[ADR 0009](0009-quality-history-storage-and-access.md)'s dashboard. A CI job
holds neither key: it presents the GitHub Actions OIDC token GitHub minted for
that run to a relay, which verifies it, decides what may be written from the
claims, mints an installation token scoped to exactly that, posts, and discards
it.**

## The write half was recorded nowhere

ADR 0009 argues the App-over-OAuth case for *reading*: an OAuth App's `repo`
scope is all-or-nothing, granting read and write on every private repository the
user can reach, which is indefensible for a tool whose argument is that it holds
none of your data. The same reasoning carries to writing without modification.
An App asking for pull-request write on repositories chosen at install time is a
bounded, revocable, per-repository grant; a personal token is neither.

ADR 0013 and ADR 0015 do not mention an App at all, and ADR 0009 explicitly
keeps publishing out of the CLI. So the identity that writes was the one part of
the loop nothing had decided.

## Two Apps, because posting a comment needs no repository read

Rendering a comment reads report documents a CI job produced. Posting it needs
`pull-requests: write`. Neither step opens a source file, so a tool arguing it
holds none of your data must not ask for `contents: read` in order to write a
comment.

Two Apps means two private keys in two Workers. A compromised posting relay
cannot read anyone's source, because it holds no credential that can. One App
serving both halves would collapse that: every consumer who wanted a comment
would have granted repository read, and the relay that posts would hold the key
that reads.

That separation is only real if the credentials are actually separate at rest.
**Each Worker gets its own secrets, and Cloudflare's account-level Secrets Store
is deliberately not used.** A shared store hands both Workers both credentials
and leaves the two-App split as an intention rather than a boundary.

## The vendor operates them

The Codecov and Snyk model: consumers install an App and never hold a private
key. An earlier proposal had each consumer register their own App from a
manifest, and it was rejected twice over — it is not how these products work,
and it pushes key custody onto every consumer, which is strictly worse for the
people least equipped to carry it.

The cost is accepted and named: lydite runs infrastructure. Two Workers, a
domain, and a key to rotate. ADR 0009 already accepted the Worker as "real
infrastructure, small but ours to keep up"; this is one more of the same, not a
new class of obligation.

## This narrowly revises ADR 0009, and only narrowly

What ADR 0009 defers is a **lydite-operated multi-tenant store** — "a central
store of unremediated vulnerabilities across other people's private
repositories", with its auth, tenant isolation, storage cost and incident
response, where one cross-tenant leak is existential. That deferral stands, and
this decision does not touch it.

A relay that stores nothing is not that. It holds no database, no cache and no
log of a request body, and every token it mints lives for the one request that
minted it. What passes through it is a comment somebody's CI already rendered
about their own pull request, on its way to that pull request. lydite is a
conduit for one HTTP request, not a custodian.

The distinction is worth stating precisely because it is easy to erode. Adding a
cache "for rate limits", or a log line "for debugging", is the step that turns
this into the thing ADR 0009 deferred.

## A token instead of a credential takes the comment write out of the gating job

`lydite test --gate-coverage` runs each component's suite and any `setup` and
`teardown` shell the declaration carries, through `sh -c`. On a pull request that
is the pull request's own code. Recording a coverage baseline needs a token that
can push. A job doing both at once is arbitrary write access to the repository
for anyone who can open a pull request — which this repository worked around by
running the gate read-only and paying an instrumented measurement of the base
tree on every change.

The OIDC token removes one of the two writes from that job, and leaves the other
exactly where it was. Posting the comment needs no credential in CI: the relay
takes the repository from the `repository` claim and never from the request body,
cross-checks the submitted pull-request number against `ref`, and mints a token
narrowed to that one repository and to `pull_requests: write`. So the worst a
pull request's own code can achieve through the relay is a wrong comment on its
own pull request — addressed to the person who wrote it.

**Recording a baseline is untouched, and this ADR closes nothing about it.** That
write is a push to the `lydite` branch, which needs `contents: write`; the relay
mints `pull_requests: write` and has no endpoint that commits anything, so it
cannot record a baseline on a job's behalf. A gating job that both runs the
repository's code and records still holds a pushing token. What resolves that is
either the split [#49](https://github.com/lydite/lydite/issues/49) describes — a
second command that writes what a first measured, executing nothing from the
repository — or a relay endpoint that does not exist. Until one of the two lands,
this repository keeps running the gate read-only and paying for the base tree on
every change.

Four checks, each closing a distinct hole: the signature, so the token is
GitHub's; `iss`, so it is Actions' and not another issuer's; a declared `aud`, so
a token minted for a different service cannot be replayed; and `exp`, so one
captured from an old run cannot be reused. The algorithm is fixed at RS256 rather
than read from the token, because honouring a token's own choice is how a
verifier ends up accepting `alg: none`. A rejection carries no detail: a verifier
that says which check failed tells an attacker how to get one step closer.

`ref` is load-bearing and worth naming. It is the only statement of which pull
request a run belongs to that the run cannot choose, which is why the pull-request
number is read from it rather than merely compared against it, and why a push
build — whose ref is not `refs/pull/<n>/merge` — can comment on nothing.

## The `github-token` fallback is a required path

A consumer who has not installed the App still gets a comment, posted with the
workflow's own token as `github-actions[bot]`. So does one whose request the relay
could not serve.

This is not a degradation to be removed later. It is what lets the App be
genuinely optional — an identity is a nicety, and a gate that only reports its
verdict to consumers who installed something is a gate that silently reports
nothing to everyone else. It also means the relay's absence is never an outage.

For that reason a repository the App is not installed on is answered with *an
answer* rather than an error. The relay says so and names the fallback; treating
it as a failure would make the ordinary state of every repository that has not
opted in look like something going wrong.

The comment is found by a marker in its body rather than by author, which is what
lets the two paths hand over: a relay taking over a comment the fallback posted
edits it instead of leaving two standing verdicts on the pull request.

## TypeScript, and no runtime dependencies

Both Workers sign an App JWT, so the security-relevant code should exist once
rather than in two languages that agree today. One language makes that possible;
`libs/github-app` makes it true.

`workers-rs` was considered and rejected on the published behaviour rather than
on taste: it compiles to WebAssembly through `wasm-bindgen`, and Cloudflare's own
documentation warns that unoptimised Wasm binaries "may exceed Worker bundle size
limits or experience long startup times". For one verify, one sign and two
`fetch`es — where WebCrypto is native runtime code — Rust adds cold-start and
buys no throughput.

Zero runtime dependencies follows from the same fact: WebCrypto covers RS256
verify *and* sign, which is the whole of the cryptography here.
`@cloudflare/workers-types` is deliberately not even a development dependency.
Nothing here uses a Cloudflare-specific type, and wrangler declares a peer range
on that package — which is exactly the coupling that removed ESLint from lydite
(see [ADR 0008](0008-biome-as-the-only-typescript-linter.md)): a Dependabot bump
to either side crossing the other's range makes `npm ci` fail with `ERESOLVE`,
and the failure lands on whoever runs it next rather than on the bump.

## The key is PKCS#8 before it is stored, not at runtime

GitHub issues an App's private key as PKCS#1 — the envelope reads `BEGIN RSA
PRIVATE KEY` — and `crypto.subtle.importKey` accepts only PKCS#8. The conversion
is one `openssl pkcs8 -topk8 -nocrypt` against the file GitHub hands you, done
once, and the PKCS#8 form is what the secret holds.

Converting at runtime was rejected. It means parsing ASN.1 and re-wrapping it in
the one place in the repository that handles a private key, for a transformation
with no reason to happen more than once in the key's life. So a PKCS#1 key is
refused — by the secrets-sync workflow before it is stored, and by the library if
one reaches it — naming the command that fixes it. An opaque import failure would
send somebody looking at their key's contents, which is the last thing anybody
should be doing.

## The Apps configure nothing yet

Making `lydite/referral` a required status check is the thing an App would
naturally do on install, the way Codecov does, and it is what
[#34](https://github.com/lydite/lydite/issues/34) is about. It needs
`administration: write`, which neither App requests. Until either that or gt's
side of #34 lands, the referral status is published and flipped correctly and
blocks no merge. That is a stated temporary state, not the design.
