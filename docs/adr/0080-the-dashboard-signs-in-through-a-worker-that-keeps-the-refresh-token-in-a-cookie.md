# The dashboard signs in through a Worker that keeps the refresh token in an HttpOnly cookie

[ADR 0009](0009-quality-history-storage-and-access.md) has the dashboard read the ledger with the
*viewer's* GitHub credentials, so lydite holds none of anyone's data. A GitHub App's user-to-server
flow needs a client secret to exchange a code, and a static bundle cannot hold one. Something
server-side must therefore take part in the sign-in. `source/cloud-services/oauth-exchange` is that
something. This records what it holds, what it refuses to hand over, and the requirement it places
on the dashboard that calls it.

[ADR 0022](0022-a-vendor-operated-app-and-an-oidc-relay.md) already records that lydite operates
two GitHub Apps, and why: `lydite` (pull-request write, behind the `pr-relay`) and
`lydite-dashboard` (`contents: read` only, for the viewer's sign-in). `oauth-exchange` holds only
`lydite-dashboard`'s client ID and client secret. The App has no private key and no webhook, and its
user tokens expire.

## Decision

The Worker drives the whole OAuth round trip on one origin, `app.lydite.org`, answers exactly four
routes, and returns 404 for everything else.

### The refresh token never reaches browser JavaScript

GitHub's token reply carries a refresh token that lives for months. The Worker strips
`refresh_token` and `refresh_token_expires_in` from every reply and sets the token as
`__Host-lydite_refresh`: `HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/`, with `Max-Age` equal to
`refresh_token_expires_in`. No script on the page can read it, and another site cannot have the
browser present it. A GitHub reply missing either the token or its lifetime answers 502 and sets no
cookie: it is an App without expiring user tokens, and the session it would start could never be
renewed.

### Sign-in is Worker-driven, with `state` and a PKCE verifier bound to the browser

- `GET /auth/login` generates a fresh `state` and a PKCE S256 verifier, binds them to this browser
  in `__Host-lydite_login` (`HttpOnly`, `Secure`, `Path=/`, `Max-Age=600`), and redirects to
  GitHub's authorize URL with the S256 challenge. That cookie is `SameSite=Lax`, not `Strict`,
  because GitHub's redirect back is a cross-site top-level GET and a `Strict` cookie would not be
  sent on it.
- `GET /auth/callback` clears the login cookie on every outcome, so a failed sign-in starts again
  with a fresh pair. With no login cookie it redirects to `/auth/login` and never exchanges the
  code, since nothing binds that code to the viewer presenting it. A `state` mismatch answers 400.
  An `error` parameter or a missing `code` redirects to `/`. A failed exchange answers 502 and a
  refused grant (GitHub answers 200 with an error body) answers 400. On success it sets the refresh
  cookie and redirects to `/`. The first access token is discarded, so no token ever appears in a
  URL.
- `POST /auth/refresh` trades the refresh cookie for an access token and rotates the cookie to the
  successor GitHub issues. The body is `{access_token, expires_in, token_type}` and the response is
  `no-store`. A refused refresh grant answers 401 and leaves the cookie alone (see Rejected).
- `POST /auth/logout` clears the cookie and answers 204. The token is not revoked at GitHub; it
  expires on its own, and with the cookie gone nothing on this origin can present it.

Every `__Host-` clear carries `Secure; Path=/; HttpOnly; Max-Age=0`. A browser ignores a `__Host-`
cookie's deletion unless it is `Secure` with `Path=/`.

### Refresh and logout answer only their own origin

`POST /auth/refresh` and `POST /auth/logout` act on a cookie the browser attaches by itself, so
both answer 403 unless `Origin` equals the request URL's origin. A request with no `Origin` is
refused, because a browser sends one on every cross-origin POST and the dashboard's own `fetch`
sends one too. This check does not depend on the browser honouring `SameSite`. The Worker never
sends a CORS header, so no other origin can read what either route answers.

### The Worker stores and logs nothing

It has no KV, Durable Object, D1 or Cache API binding and writes no `console` output. Every
rejection carries a generic body: none echoes a code, a state or a token, so none can land in a log
or a proxy cache. The access token appears only in the `/auth/refresh` body.

### The host is the Custom Domain `app.lydite.org`

The Worker is bound to `app.lydite.org` as a Custom Domain and answers `/auth/*` only, so `/` is
404 until the dashboard bundle ([#331](https://github.com/lydite/lydite/issues/331)) arrives as a
static-assets binding on this same Worker. The sign-in and the app then share an origin, which the
`__Host-` cookies and the origin check both rely on. The redirect URI is derived from the request's
own origin, so no URL is configured.

### A requirement on the dashboard, recorded and not built

A refresh rotates the refresh token **and invalidates the previous access token**. Two tabs
refreshing independently each invalidate the other's token, and the loser holds a dead token it
believes is live. The dashboard (#331) must therefore coordinate its tabs:

- one refresh at a time across tabs, under a Web Lock;
- the result shared with every tab over a `BroadcastChannel`;
- one retry of a refresh that answered 401 before sending the viewer to `/auth/login`, because that
  401 is most often a sibling tab's rotation landing between this tab's read of the cookie and its
  request.

None of this lives in the Worker, which holds no state a tab could coordinate through.

## Rejected

**Relaying the refresh token in the response body.** The dashboard would keep it in script-reachable
memory or storage. Any XSS on the dashboard would then read a months-long credential, where with
the cookie it can read at most a short-lived access token.

**Browser-driven sign-in with `state` in `sessionStorage`.** The client secret still needs a
server, so a Worker is involved anyway. The `state` check and the PKCE verifier would live in
script that any XSS controls, which is the script an attacker needs to defeat to forge a login.
Binding both in an `HttpOnly` cookie puts them out of its reach.

**A zone path route (`lydite.org/auth/*`) instead of the Custom Domain.** It requires the host to
already be proxied by something else that serves the rest of its paths, so the sign-in's and the
app's responses would come from two deployments sharing one origin's cookies. Anything else served
on that origin would be same-origin with the refresh endpoint, passing its `Origin` check and sitting
inside the cookie's scope. A Custom Domain gives the Worker the whole host and supplies DNS and TLS
itself, and the dashboard bundle can later arrive as a static-assets binding on the same Worker, so
one deployment owns the whole origin.

**Clearing the cookie on a refused refresh grant.** GitHub rotates the refresh token on every grant,
so under concurrent tabs a refused grant is most often a token a sibling tab has just spent, whose
response has already set the successor. Clearing the cookie would sign every tab out. A viewer whose
token is truly dead is sent to `/auth/login` by the dashboard's retry instead.

## Consequences

- A compromised dashboard bundle can use an access token for its short lifetime but cannot exfiltrate
  the credential that mints new ones.
- `oauth-exchange` holds one secret, `LYDITE_DASHBOARD_CLIENT_SECRET`, for an App with
  `contents: read` only. A compromise of the Worker cannot post to a pull request or read a
  repository the viewer cannot.
- Logout is local: a copied refresh token stays valid at GitHub until it expires.
- The dashboard cannot work correctly across multiple tabs until it implements the coordination
  above.
