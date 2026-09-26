---
about: no issue_comment payload alone reaches the clearance flow's decision or any status document — every path, even "not a pull request" and "not addressed to lydite", now needs two live GitHub reads first, and a clearance needs five before Decide; so it cannot be exercised end-to-end in CI without an API stub
saw:
  - source/cli/cmd/lydite/clearance.go
  - source/cli/internal/flows/clearance/clearance.go
  - source/cli/internal/stages/scm/scm.go
  - source/cli/internal/forge/event.go
  - source/cli/internal/forge/calls.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - source/cli/internal/stages/clearance/clearance.go
---

Re-checked on `refactor/flow-architecture-clearance-pilot`, where `runClearance` became a thin
driver of `internal/flows/clearance`. The claim got **stronger**: it was "three live reads
before any document write, with two refusals reachable offline"; now no answer at all — not even
the two cheap refusals — is reachable without network.

**Offline refusals (none writes anything, all exit 1):** no event path; a payload
`forge.ReadCommentRef` rejects (no `comment.id` or no `repository.full_name` — it reads nothing
else from the payload); `InitTrust` (`trust.FromEnvironment` needs `GITHUB_REPOSITORY`);
`InitSCM` (`scmstages.ErrNoCredential` without `GITHUB_TOKEN`/`GH_TOKEN`); `LoadComment` refusing
a payload whose claimed repository differs (case-insensitively) from the trusted one.

**Live reads, in flow order** (`internal/flows/clearance/clearance.go`):
1. `load-comment` → `forge.Client.IssueComment`: `GET /repos/:o/:r/issues/comments/:id`, then
2. `GET /repos/:o/:r/issues/:n` — the only way to tell a PR from a plain issue, since the
   comment names its thread only by issue URL. Body, author, time and PR number all come from
   these reads, never from the payload.
   → `parse-command` now decides "not a pull request" / "not addressed" only after these two.
3. `resolve-head` → `HeadSHA`, 4. `check-permission` → `CanWrite`, 5. `read-referral` →
   `ReferralStatus` — all `When(addressed)`, all before `decide`.
   Then, lazily: `ChangedPaths` inside `Decide` for `/lydite exempt`; `PullRequestTitle` inside
   the `fingerprint` stage for `/lydite clear` (only once `checkoutIsHead` and base resolution
   pass).

Confirmed empirically: a built `lydite clearance --event <payload> --status-out out.json` with
`GITHUB_TOKEN`/`GITHUB_REPOSITORY` set and `GITHUB_API_URL=http://127.0.0.1:1` fails with
`reading comment 42 on o/r: ... connection refused` and writes no document; the same run with
`GITHUB_REPOSITORY` naming a different repo fails offline with the foreign-repository refusal.

So the rendered clearance's two-document contract (`TestARenderedClearanceAlsoRendersTheResolvedReferral`
in `cmd/lydite/clearance_test.go`) is covered only in-process via `fakeForge`'s `httptest` stub
reached through `GITHUB_API_URL`; the stages themselves are covered against a fake
`SCMRepository` in `internal/stages/clearance/fake_test.go`. A CI end-to-end assertion driven
by a synthetic payload alone cannot reach `RenderStatuses`; it needs the same loopback stub —
a new convention for `.github/workflows/` (tracked as lydite/lydite#236, not yet adopted), which
now has to serve the comment and issue endpoints too.
