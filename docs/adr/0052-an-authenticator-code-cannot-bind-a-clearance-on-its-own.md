# An authenticator code cannot bind a clearance on its own

[#25](https://github.com/lydite/lydite/issues/25) proposes `/lydite clear <code>`, a TOTP code
from a standard authenticator app, as a second factor closing the gap `CanWrite`'s doc comment
names explicitly: push permission is "not the whole of the trust — whoever holds the
repository's credentials satisfies it." The gap is real. The adversary is not an outsider —
`CanWrite` already stops those — it is **an agent holding a maintainer's already-issued
credentials**: a process with `GH_TOKEN` or an equivalent, able to read a pull request's
comments and post one back within roughly the latency of the human it is impersonating, well
under a TOTP's 30–90 second validity window.

We are not building it. The reason is not that burn-on-use is missing — #25 already scopes that
in — it is that burn-on-use, once built, does not deliver what a "security boundary" has to
deliver against this adversary.

## Why burn-on-use does not close the race

A standard authenticator app computes `TOTP(secret, time)` alone; it cannot incorporate a PR
number or a SHA. So a code is valid for any PR during its window, and the scenario #25 opens
with is real: the human posts `/lydite clear 483920` on their PR, the adversary reads that
comment and posts the same code against a different PR before it expires.

Burn-on-use — record consumed codes, refuse reuse — is meant to close exactly that. But the
human's comment and the adversary's copied comment each trigger their own, independently
scheduled `issue_comment` workflow run. Whichever run reaches the atomic "burn this code" step
first wins; nothing makes the legitimate run deterministically first. GitHub Actions runner
cold-start and relay round-trip latency are not ordered in the human's favor — the human is
typing and posting once, unassisted; the adversary is a program reacting to a webhook. First-use-
wins converts "any code works for anyone within 90 seconds" into "first arrival wins a race with
no guaranteed winner." That is a real narrowing over the unbound case — it defeats casual or
delayed misuse, a code pasted into Slack and reused later — but it is not a boundary against the
adversary this ADR names in the first paragraph, who is present at the moment the code is
generated and faster than the human whose action it is racing.

## What it would cost to build anyway

Even setting the race aside, three pieces of real infrastructure stand between #25 and a working
`/lydite clear <code>`, none of which exist today:

- **The burn list fits neither of CONTEXT.md's two storage categories.** #25 claims the `lydite`
  branch is "a Cache by CONTEXT.md's own definition." It is not: a Cache's losses cost time,
  because the data is regenerable by re-running the tool that produced it. A burned code cannot
  be regenerated — the lost fact *is* the security guarantee, not a derived measurement — so it
  is not a Cache. It is not quite a Ledger either, since a Ledger's invariant is
  never-recomputable-but-append-only-and-complete; what a burn list needs on top of that is
  mutual exclusion between two concurrent claims on the same code, which `gitstate.Write`'s
  push-retry loop does not provide today (concurrent writers retry-race; there is no
  first-writer-wins semantics keyed on a value). This is new state with an integrity requirement
  neither existing mechanism was built to hold.
- **Writing it needs surface neither the clearance job nor pr-relay grants today.** The clearance
  job runs with `contents: read`, and its own workflow header says explicitly that nothing in it
  may start doing anything a writing token should not be trusted with — widening it to
  `contents: write` directly contradicts that design. The relay route that exists after #211 is
  not a substitute: pr-relay's own module doc states it never mints `contents: write` and has no
  push or ref-write endpoint at all — today it posts exactly three narrowly-scoped things
  (a comment, a review, one of two named commit-status contexts). Giving it a fourth capability —
  writing arbitrary ref content under the App's identity — is a real widening of what the
  vendor-operated relay can do, for every consumer of it, not only for this feature.
- **The secret cannot live where #25 puts it.** #25 says only "verification runs from the base
  branch," which reads as the clearance job's own CI process holding the secret. But the Q1
  adversary holds a write-scoped token — enough to push a new workflow to any branch and dispatch
  it via the API on that ref — and an ordinary repository or organization Actions secret is
  readable by any workflow that adversary can run, regardless of which branch it lives on. A
  secret sitting in the clearance job's own environment is exactly as exposed as the credential
  it exists to defend against. The only placement that survives this adversary is holding the
  secret in the relay's own environment and never handing it to any GitHub Actions job — which
  turns TOTP verification into a new relay responsibility, not a CLI one.

None of these are reasons to abandon the feature on cost alone — they are reasons the race in
the previous section has to be worth closing before any of them are worth building. It is not:
paying for a new integrity store, a new relay write endpoint, and a new secret-custody model
still leaves the promised property racy rather than closed.

## Decision

**We are not building `/lydite clear <code>`.** A context-free TOTP cannot bind itself to the PR
or SHA it is meant to clear, and burn-on-use narrows the resulting window without closing it —
the load-bearing gap #25 itself names in its own "part that decides whether this is real"
section survives every mitigation available to a standard authenticator app.

What would change this: a **bindable second factor** — something that lets what gets verified
incorporate the PR number and SHA, such as a signature a maintainer produces on demand over
`(pr, sha)` with a private or hardware key, rather than a value computed from time alone.
Standard authenticator apps cannot supply that by construction; closing #25's gap for real means
a different mechanism, not a variant of this one.

**CODEOWNERS-scoped clearance is a different axis and a real answer to a different question.**
It restricts *who* may hold write-enough-to-clear rather than *proving* the commenter is who
they claim — narrower blast radius if a token is compromised, no race, no new storage or relay
surface. It does not close #25's stated gap (an agent holding a legitimately maintainer-scoped
token is still that maintainer under CODEOWNERS), so it is not a substitute for #25 — it is a
worthwhile, independent hardening this pass surfaced and is filing separately rather than folding
in here.

**No local-CLI verification.** Verifying the code inside the CLI running on a contributor's or
CI's own machine was raised and rejected during this pass for the same reason it was rejected
before: it breaks the App pin and the audit record clearance depends on.

#25 is closed by this ADR with `Closes #25` on the pull request that lands it; a new issue for
CODEOWNERS-scoped clearance is filed separately and is not gated on anything here.
