---
about: source/cloud-services/pr-relay/src/index.ts
kind: gotcha
---

# A trailing slash on `LYDITE_RELAY_URL` produces a 404, not a 401

Both `.github/actions/lydite-comment/action.yml` and `.github/actions/lydite-threads/action.yml`
build the relay request as `${RELAY}/comment` (or `/review`) and separately mint the OIDC token
with `audience=${RELAY}` — the same input feeds both the request path and the token's `aud`
claim. `source/cloud-services/pr-relay/src/index.ts`'s handler checks the route
(`!ROUTES.includes(route)`, currently :111 — `ROUTES` gained a third entry, `/status`, when the
relay learned to post commit statuses, but the check's position relative to verification did
not move) before it ever verifies the token (`bearer(request)` at :115,
`verifyActionsToken` at :122). A trailing slash on `RELAY` turns the request path into a double
slash (`https://pr.lydite.org//comment`), which `new URL(...).pathname` does not collapse, so
the route check fails and the handler returns 404 (the body now reads
`{"error":"POST /comment, POST /review or POST /status"}`) before the audience comparison is
reached at all.

**Saw:** verified live on `lydite/lydite` PR #153 — pointing `lydite-pr.yml`'s `relay:` inputs
at `https://pr.lydite.org/` (trailing slash) produced exactly this 404, not the 401 an audience
mismatch would give (run
https://github.com/lydite/lydite/actions/runs/34934869284/job/104271143368). `docs/adr/0037-...`
initially claimed the trailing-slash case was a 401 and had to be corrected once this was
observed.

A genuine audience mismatch (401) needs a `LYDITE_RELAY_URL` that is well-formed as a URL path
(so `${RELAY}/comment` still resolves to `/comment`) but differs from the relay's own
`AUDIENCE` in scheme, host, or port — not a trailing slash, which breaks routing first.
