---
about: pr-relay's /status route has no shape that admits a merge_group-triggered run; only pull_request-ref runs and issue_comment clearance runs are recognized
saw:
  - source/cloud-services/pr-relay/src/index.ts
---

Read while researching ADR 0053 (clearance survives the merge queue).

`resolveTarget` (`source/cloud-services/pr-relay/src/index.ts`, ~line 350) derives the target
pull request two ways only: `pullRequestFromRef(claims.ref)` (parses `refs/pull/<n>/merge` or
`refs/pull/<n>/head` — see `source/cloud-services/libs/github-app/src/oidc.ts:136`), or, when
that fails and the route isn't `/review`, `clearanceRun`'s exemption, which requires
`claims.event_name === "issue_comment"` and `claims.ref` starting with `refs/heads/`. A
`merge_group` run's ref is `refs/heads/gh-readonly-queue/<base>/pr-<n>-<sha>` — `pullRequestFromRef`
returns undefined for it (it only matches `refs/pull/...`), and `clearanceRun` also refuses it
(`event_name` is `merge_group`, not `issue_comment`). So `resolveTarget` returns
`{error: "this run is not for a pull request, so there is nothing to write to"}` for *any*
merge_group-triggered call to `/status` (or `/comment`), regardless of payload — the relay route
is a dead end for a queue-time write today, independent of whatever new identity model gets
designed. A queue-time republish either goes through a job's own `statuses: write` token (the
same route `referral-publish`/`clearance` use directly per ADR 0051) rather than the relay, or
the relay needs a new admitted shape for `merge_group` + `job_workflow_ref` before it can carry
this write.
