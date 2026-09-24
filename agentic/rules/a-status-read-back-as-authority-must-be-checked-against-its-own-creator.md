# A status read back as authority must be checked against its own creator, not just its context and state

A context name is not evidence of who wrote it: any token holding `statuses: write` can post
under `lydite/clearance`, and a fingerprint embedded in a description
(`internal/clearance.WithFingerprint`) is a hash of the change's own diff that anybody can
recompute and paste into their own status. Reading a status by `context` and `state` alone, the
way `/status`'s `currentStatus` already did for `lydite/referral`, lets an author forge the
status their own entry is then cleared by — exactly what an allowlisted `job_workflow_ref` exists
to prevent for the *write* path, and nothing enforces for the *read* path unless the read checks
who posted the status being trusted. `/merge-group`'s `queueVerdict` requires
`cleared.creator?.login === APP_STATUS_CREATOR` (`<app-slug>[bot]`) before treating a
`lydite/clearance` status as carryable authority; a status any other identity wrote answers
`pending`, the same as no clearance at all, never an error — a repository whose clearance was
recorded some other way (the `github-token` fallback) has an entry to clear again, not a broken
mechanism.

## Applies to

Any `pr-relay` route that reads a standing commit status and uses it to decide what gets
published, rather than merely relaying a caller's own assertion — `standingStatus`'s callers in
`source/cloud-services/pr-relay/src/index.ts`, and any future route with the same shape.

## Example

```ts
// wrong: a context name and a state are enough to trust a status as authority
const cleared = await standingStatus(token, repo, head, CLEARANCE_CONTEXT, fetcher);
if (cleared?.state === REFERRAL_RESOLVED) { /* carry it forward */ }

// right: only this App's own bot identity may have posted the status being trusted
if (cleared?.state !== REFERRAL_RESOLVED || cleared.creator?.login !== APP_STATUS_CREATOR) {
  return { state: REFERRAL_PENDING, description: clip(`...clear #${pull} again`) };
}
```

Reasoning: [`agentic/references/surface.md`](../references/surface.md).
