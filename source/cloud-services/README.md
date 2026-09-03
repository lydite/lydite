# cloud-services

The two Workers lydite operates, as one npm workspace — see
[ADR 0022](../../docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md).

| Worker | Origin | Holds |
|---|---|---|
| `pr-relay` | `pr.lydite.org` | the `lydite` App's private key |
| `oauth-exchange` | `app.lydite.org` | the `lydite-dashboard` App's client secret |

`libs/github-app` is shared: App JWT signing, installation tokens, and the
Actions OIDC verification. One implementation, because it is the security-
relevant part and two would agree today and drift later.

## Why there are no runtime dependencies

WebCrypto covers RS256 verify *and* sign, which is the whole of the
cryptography here, and it is native runtime code rather than a bundle. So
nothing is installed to run — the dependencies below are all development ones.

`@cloudflare/workers-types` is deliberately **not** among them. Nothing here
uses a Cloudflare-specific type: the Workers runtime API this code touches is
`Request`, `Response`, `fetch` and `crypto.subtle`, which are the standard
library. And wrangler declares a peer range on that package, which is exactly
the coupling that removed ESLint from lydite — a Dependabot bump to either side
crossing the other's range makes `npm ci` fail with `ERESOLVE`, and the failure
lands on whoever runs it next rather than on the bump.

`vitest` is pinned at the version lydite's own `vitest` runner was verified
against. lydite pins the tools it runs and not the ones a repository declares,
so this pin is this workspace's own statement.

## Running it

```sh
npm ci                      # the frozen install lydite would do
npx vitest run              # the suite
npx tsc --noEmit            # what the build-only variant checks
npx wrangler dev            # from pr-relay/ or oauth-exchange/
```

`lydite test --dir ../..` runs the suite the way CI does, and measures it.

## Secrets

Set by `.github/workflows/workers-secrets-sync.yml`, from GitHub, which is the
single source of truth. **Each Worker receives only its own App's credential**
— per-Worker secrets rather than an account-level store, so `pr-relay` cannot
read the dashboard's. That is the whole reason there are two Apps.

`LYDITE_APP_PRIVATE_KEY` must be **PKCS#8**. GitHub issues an App key as
PKCS#1 and WebCrypto imports only PKCS#8:

```sh
openssl pkcs8 -topk8 -nocrypt -in app.pem -out app.pkcs8.pem
```
