---
about: "oauth-exchange already exists as an unguarded, undeployed scaffold: no state/origin check, no PKCE, no static serving, refresh token in the body, not in the deploy matrix"
saw:
  - source/cloud-services/oauth-exchange/src/index.ts
  - source/cloud-services/oauth-exchange/wrangler.toml
  - .github/workflows/workers-deploy.yml
  - .github/workflows/workers-secrets-sync.yml
  - docs/adr/0009-quality-history-storage-and-access.md
  - docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md
---
Cost: finding that issue-329-shaped work is a hardening of an existing Worker, not a new one.

- `oauth-exchange/src/index.ts` (85 lines) answers only `POST /token`; body `{code}` or `{refresh_token}`, relays GitHub's JSON verbatim (access_token, refresh_token, expires_in) to the caller, 400 on a 200-with-error body, 502 on non-ok. It reads no `state`, no Origin/Referer, sends no `redirect_uri` or PKCE verifier, sets no CORS or cookie headers. Header comment says "It is a scaffold".
- wrangler.toml: custom domain `app.lydite.org` (whole host, not a path route); vars `LYDITE_DASHBOARD_CLIENT_ID = ""`; secret `LYDITE_DASHBOARD_CLIENT_SECRET` (no private key, by design).
- Not in `workers-deploy.yml` matrix (`worker: [pr-relay]`, comment cites #27): it has never deployed. The deploy and secrets-sync scripts already have `oauth-exchange)` case branches. Re-adding means editing the matrix list only.
- Tests: `oauth-exchange/src/index.test.ts` (4 cases: 404, 400, code+refresh grant types, GitHub 200-error).
- No ADR or reference decides token storage in the browser, state generation, PKCE, or cookie-vs-body for the refresh token (grep for pkce|sessionStorage|cookie|CORS over docs/adr, surface.md, quality-history.md: nothing relevant). ADR 0009 only decides: GitHub App not OAuth App; Worker "does nothing but exchange an OAuth code for a token"; stores nothing; app and callback share origin app.lydite.org so the exchange never crosses origins; .org chosen over .io. ADR 0022: two Apps, per-Worker secrets, no account-level Secrets Store, no cache or log "for debugging".
- Nothing serves a static bundle yet (source/web is empty); `app.lydite.org` as a custom domain terminates on the Worker, so a same-origin dashboard means the Worker (or a Workers static-assets binding) must serve it.
