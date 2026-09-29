---
name: relay-url-trailing-slash-produces-404-not-401
kind: gotcha
description: pr-relay's route check runs before token verification, so a trailing slash on LYDITE_RELAY_URL turns the request path into a double slash the route check rejects (404), never reaching the audience comparison a real mismatch would fail with (401).
anchors:
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: cccaa62747de
confidence: suspect
---

`.github/actions/lydite-comment/action.yml` and `lydite-threads/action.yml` build the relay request
as `${RELAY}/comment` (or `/review`) and separately mint the OIDC token with `audience=${RELAY}` —
the same input feeds both. `pr-relay/src/index.ts`'s handler checks the route (`!ROUTES.includes(route)`)
before it ever verifies the token (`bearer(request)`, `verifyActionsToken`). A trailing slash on
`RELAY` turns the request path into a double slash (`https://pr.lydite.org//comment`), which
`new URL(...).pathname` does not collapse, so the route check fails and the handler returns 404
before the audience comparison is reached at all.

Verified live on `lydite/lydite` PR #153: pointing `relay:` inputs at a trailing-slash URL produced
exactly this 404, not the 401 an audience mismatch gives — `docs/adr/0037-...` initially claimed
401 and had to be corrected. A genuine audience mismatch (401) needs a `LYDITE_RELAY_URL` that is
well-formed as a URL path but differs from the relay's own `AUDIENCE` in scheme, host, or port —
not a trailing slash, which breaks routing first.

(Suspect: the `ROUTES`/ordering claim is checkable in `index.ts` but not independently re-verified
in this pass; the live-run evidence cannot be re-run from the repo alone.)
</content>
