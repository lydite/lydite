---
name: actions-claims-sha-is-the-synthetic-merge-commit
kind: gotcha
about: source/cloud-services/libs/github-app/src/oidc.ts
description: ActionsClaims.sha, on a pull_request-triggered run, is GitHub's synthetic merge commit rather than the pull request's real head — the same trap forge.PullRequestEvent (the Go side) already reads a head from the event payload to avoid, but the TypeScript relay had no equivalent guard until this was found in review.
anchors:
  - path: source/cloud-services/libs/github-app/src/oidc.ts
    blob: c3affd4a98594e4ba4c8bcba50c1c0dc54e15425
  - path: source/cli/internal/forge/event.go
    blob: 78eaa9747cf50194825002dae2e382319f3e22d0
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: 2b5c7e42e4337a2db55878c4c75efccde25e35ba
confidence: verified
---

`ActionsClaims.sha` is populated straight from the GitHub Actions OIDC token's `sha` claim,
which mirrors `GITHUB_SHA`. On a `pull_request` event, that value is the platform's synthetic
merge commit — a revision that exists on no branch — not the pull request's actual head. The
Go CLI already routes around this: `forge.PullRequestEvent` (`source/cli/internal/forge/event.go`)
deliberately reads the head from the event payload's `pull_request.head.sha` rather than from
`GITHUB_SHA`, with a comment explaining why publishing against the merge commit would put a
verdict "somewhere nobody looks".

`pr-relay`'s new `POST /status` endpoint (added in the same branch this was found on,
`source/cloud-services/pr-relay/src/index.ts`) first tried requiring a caller-submitted `sha`
to equal `claims.sha` directly, on the theory that binding it to the verified claim was as safe
as `repository` and `ref` are. That requirement refused every legitimate call, because a real
`lydite/referral` publish sends the pull request's actual head — never the claim's merge
commit — so the two are never equal on genuine traffic. A `panel-code-review` correctness pass
caught it before merge; the fix resolves the pull request's live head via
`GET /repos/:repo/pulls/:n` with the installation token and compares against that instead of
against `claims.sha`.

Anyone adding a new `pr-relay` endpoint that has to bind a caller-submitted revision to "the run
this claim is for" should resolve the pull request's head live (as `/status` and `/review`'s
comment-membership check both now do) rather than compare against `claims.sha` — that claim is
reliable for identity (`repository`, `ref`) but not for revision on a `pull_request` run.
