---
name: pullrequestfromref-also-accepts-head
kind: gotcha
description: pullRequestFromRef accepts refs/pull/<n>/head as well as /merge, which its doc comment and every test claim omit.
anchors:
  - path: source/cloud-services/libs/github-app/src/oidc.ts
    blob: 4429cdf768fc
confidence: verified
---

The regex is `/^refs\/pull\/(\d+)\/(merge|head)$/`
(`libs/github-app/src/oidc.ts:116`). The doc comment directly above it
(`oidc.ts:109-114`) names only `refs/pull/<n>/merge` — "what `pull_request` events check
out, and ... the only statement of which pull request a run belongs to that the run
itself cannot choose" — and says nothing about `/head`. Neither
`pr-relay/src/index.test.ts` nor `libs/github-app/src/oidc.test.ts` exercises the
`/head` form; every test claim uses `refs/pull/7/merge`.

So a reader trusting the comment, or trusting the tests as the statement of the grammar,
would believe `/head` is rejected the way `refs/heads/main` is (tested, 403). It is not:
`/head` parses and the relay treats it as a legitimate pull-request run. Whether that is
intentional slack (a caller checking out `/head` to avoid the synthetic merge commit) or
leftover permissiveness is not decided anywhere in the code or docs. This is the trust
boundary for which refs may post a comment — do not reason about it from the comment or
the test names.
