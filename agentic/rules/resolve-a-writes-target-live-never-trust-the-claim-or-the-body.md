# Resolve a write's target live, and check it — never trust the OIDC claim or the request body alone

`/review` refuses any `reply` or `delete` naming a comment id outside the pull request's own,
freshly listed, review comments; `/status` refuses a `sha` that does not equal the pull
request's current head as `GET /pulls/:n` answers it, resolved with the installation token
rather than read from `claims.sha` — which on a `pull_request` run is the platform's synthetic
merge commit, existing on no branch and never the revision a verdict is published against.
Neither the verified claim nor the caller's own body is trusted by itself for an identifier
that names *what gets written to*: the claim can be stale or synthetic, and the body is the
caller's unverified assertion. Every new `pr-relay` endpoint that writes to something identified
by an id, a sha, or a name has to resolve the live value through the App's own token and compare,
refusing the request outright when the two disagree, rather than writing to whatever the body
named.

## Applies to

Any new route in `source/cloud-services/pr-relay/src/index.ts` that writes to a target named by
an identifier in the request body (a comment id, a commit sha, a check run id, a branch name).

## Example

```ts
// wrong: writes to whatever the body claims
await postStatus(token, claims.repository, payload, deps.fetcher);

// right: resolves the target live, and refuses on mismatch
const head = await pullRequestHeadSha(token, claims.repository, fromRef, deps.fetcher);
if (payload.sha !== head) {
  return json(403, { error: "the sha submitted is not this pull request's head" });
}
await postStatus(token, claims.repository, payload, deps.fetcher);
```

Reasoning: [`agentic/references/surface.md`](../references/surface.md).
