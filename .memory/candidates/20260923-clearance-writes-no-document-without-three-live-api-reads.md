---
about: no `issue_comment` payload alone reaches runClearance's document-writing code — every path to it needs three live GitHub API reads first, so it cannot be exercised end-to-end in CI without a stub
saw:
  - source/cli/cmd/lydite/clearance.go
---

`runClearance` has exactly two refusal branches reachable before any network call — a comment on
a plain issue (`issue.pull_request` absent) and a comment not addressed to lydite — and neither
writes a status document. Every other path, including every branch of `clearance.Decide`'s own
refusal ladder, sits behind three unconditional API calls made in sequence: `client.HeadSHA`,
`client.CanWrite`, then `client.ReferralStatus`. Confirmed empirically by pointing
`GITHUB_API_URL` at a closed port with a well-formed `/lydite clear` payload: the run fails on
the first of those three reads, before `clearance.Decide` or any document write is reached.

This is why the rendered clearance's two-document contract (`TestARenderedClearanceAlsoRendersTheResolvedReferral`,
in `clearance_test.go`) is covered only in-process, via `fakeForge`'s `httptest` stub reading
`GITHUB_API_URL` — a CI-level end-to-end assertion driven by a synthetic payload alone cannot
reach `recordClearance` at all; it would need the same loopback-stub mechanism, which is a new
convention for `.github/workflows/` (tracked as lydite/lydite#236, not yet adopted).
