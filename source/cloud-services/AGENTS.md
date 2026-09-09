# `source/cloud-services` — the Workers lydite operates

One npm workspace, and therefore one component. `pr-relay` posts the standing pull-request
comment on a consumer's behalf so that no CI job holds a credential; `oauth-exchange` is the
dashboard's. `libs/github-app` is what both mint tokens through.

Read [`surface.md`](../../.agents/references/surface.md) before changing either Worker — it
carries the relay's trust boundary, why RS256 is fixed rather than read from the token, why a
rejection carries no detail, why `ref` is the only claim a run cannot choose, and why the
`github-token` fallback is a required path rather than a stopgap. The two-App split is
[ADR 0022](../../docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md); widening either
App's permissions is what that decision exists to prevent.

This workspace declares no `@cloudflare/workers-types`: wrangler puts a peer range on it, and
a peer range crossed by a dependency bump is exactly the failure that retired ESLint — see
[`linters.md`](../../.agents/references/linters.md).

A JavaScript component must declare its own coverage provider; lydite installs none into a
workspace it is about to gate. See
[`components.md`](../../.agents/references/components.md).
