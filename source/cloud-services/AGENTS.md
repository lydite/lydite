# `source/cloud-services` — the Workers lydite operates

One npm workspace, and therefore one component. `pr-relay` posts the standing pull-request
comment on a consumer's behalf so that no CI job holds a credential; `oauth-exchange` is the
dashboard's. `libs/github-app` is what both mint tokens through.

Read [`surface.md`](../../agentic/references/surface.md) before changing either Worker — it
carries the relay's trust boundary, why RS256 is fixed rather than read from the token, why a
rejection carries no detail, why `ref` is the only claim a run cannot choose, why the `referral` and `clearance`
contexts are accepted only from an allowlisted `job_workflow_ref` (empty by default) and
a clearance run names its pull request in the body for the relay to resolve live, and why the
`github-token` fallback is a required path rather than a stopgap. The two-App split is
[ADR 0022](../../docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md); widening either
App's permissions is what that decision exists to prevent.

`POST /merge-group` is a fourth route, gated by its own `MERGE_GROUP_WORKFLOW_REFS` allowlist
(also empty by default, and disjoint from the referral and clearance allowlists — a ref in more
than one holds no authority at all). It composes `lydite/referral` itself from a fingerprint
comparison rather than relaying a caller's verdict: an unreferred entry is published `success`
directly (there is no clearance to wait on), and a referred entry is checked for a batched queue
commit before a standing `lydite/clearance` status is read at all, trusted only when that
status's own `creator` login is the lydite App's bot user — see
[the rule on checking a status's creator before trusting it as authority](../../agentic/rules/a-status-read-back-as-authority-must-be-checked-against-its-own-creator.md).
See [ADR 0053](../../docs/adr/0053-a-clearance-carries-forward-when-the-decision-it-was-given-for-is-unchanged.md).
`source/cli/cmd/lydite/mergequeue.go` is the CLI side that submits to this route
(`lydite clearance queue`); as of this writing `/lydite clear` does not yet embed a fingerprint
in the clearance it records, so a *referred* entry's merge-queue comparison answers `pending`
until that lands ([lydite/lydite#254](https://github.com/lydite/lydite/issues/254)) — an
unreferred entry is unaffected.

This workspace declares no `@cloudflare/workers-types`: wrangler puts a peer range on it, and
a peer range crossed by a dependency bump is exactly the failure that retired ESLint — see
[`linters.md`](../../agentic/references/linters.md).

A JavaScript component must declare its own coverage provider; lydite installs none into a
workspace it is about to gate. See
[`components.md`](../../agentic/references/components.md).
