---
about: "oauth-exchange serves the dashboard's sign-in on /auth/* only, keeps the refresh token in a Strict HttpOnly cookie, and must not clear it on a refused refresh grant"
saw:
  - source/cloud-services/oauth-exchange/src/index.ts
  - source/cloud-services/oauth-exchange/src/cookies.ts
  - source/cloud-services/oauth-exchange/src/pkce.ts
  - .github/workflows/workers-deploy.yml
  - docs/adr/0080-the-dashboard-signs-in-through-a-worker-that-keeps-the-refresh-token-in-a-cookie.md
---
- `oauth-exchange/src/index.ts` routes by a `"METHOD /path"` table: `GET /auth/login`, `GET /auth/callback`, `POST /auth/refresh`, `POST /auth/logout`; everything else, `/` included, is 404. `/` belongs to the dashboard bundle, which is expected to arrive as a static-assets binding on this same Worker (ADR 0080).
- `state` and the PKCE verifier live in `__Host-lydite_login` (`SameSite=Lax`, because GitHub's redirect back is a cross-site top-level GET a Strict cookie is not sent on); the refresh token lives only in `__Host-lydite_refresh` (`SameSite=Strict`) — `cookies.ts`.
- A refused refresh grant (GitHub answers 200 with an `error` body) returns 401 with **no** `Set-Cookie`: GitHub rotates the refresh token, so the refusal is usually a sibling tab having just spent it and set the successor. Clearing would sign every tab out. A test pins the absence of the header.
- `sameText` in `pkce.ts` is a hand-rolled XOR compare on purpose: `crypto.subtle.timingSafeEqual` exists only in Workers and the suite runs under plain Node vitest; `node:crypto` is absent in the Worker without `nodejs_compat`.
- oauth-exchange imports nothing from `libs/github-app` (no private key); only pr-relay does.
- It is in `workers-deploy.yml`'s matrix beside pr-relay; a deploy is a release tag, never `workflow_dispatch` (that redeploys the whole matrix from untagged main, against ADR 0010).
