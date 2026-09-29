---
name: actions-claims-sha-is-the-synthetic-merge-commit
kind: gotcha
description: ActionsClaims.sha on a pull_request run is GitHub's synthetic merge commit, not the pull request's head, so a relay endpoint binding a submitted revision must resolve the head live rather than compare against the claim.
anchors:
  - path: source/cloud-services/libs/github-app/src/oidc.ts
    blob: 148f32f2e36f
  - path: source/cli/internal/forge/event.go
    blob: 4cac32acfe47
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: cccaa62747de
confidence: verified
---

`ActionsClaims.sha` (`libs/github-app/src/oidc.ts`, field `sha?` at line 37) comes straight from the Actions OIDC token's `sha` claim, which mirrors `GITHUB_SHA`. On a `pull_request` event that is the platform's synthetic merge commit — a revision on no branch — not the pull request's head. The Go CLI routes around it: `forge.PullRequestEvent` (`internal/forge/event.go`) deliberately reads the head from the payload's `pull_request.head.sha`.

`pr-relay`'s `POST /status` first tried requiring a submitted `sha` to equal `claims.sha`, which refused every legitimate call because a real `lydite/referral` publish sends the PR's actual head. A `panel-code-review` correctness pass caught it before merge; the fix resolves the PR's live head via `GET /repos/:repo/pulls/:n` with the installation token (`pullRequest(...)`, `index.ts:362` and `:862`) and compares against that. Any new relay endpoint binding a caller-submitted revision to "this run" should resolve the head live: the claim is reliable for identity (`repository`, `ref`) but not for revision on a `pull_request` run.
