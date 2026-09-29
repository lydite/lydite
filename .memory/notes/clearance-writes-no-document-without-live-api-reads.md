---
name: clearance-writes-no-document-without-live-api-reads
kind: gotcha
description: No issue_comment payload alone reaches the clearance decision or any status document; every path needs two live GitHub reads first, so end-to-end CI coverage needs an API stub.
anchors:
  - path: source/cli/cmd/lydite/clearance.go
    blob: 65506735cacb
  - path: source/cli/internal/flows/clearance/clearance.go
    blob: f485bdb9be4f
  - path: source/cli/internal/forge/event.go
    blob: 4cac32acfe47
  - path: source/cli/internal/forge/calls.go
    blob: 33daa8ab97cd
confidence: suspect
---

The clearance flow reads only `comment.id` and `repository.full_name` from the payload (`forge.ReadCommentRef`, `internal/forge/event.go:78`); body, author, time and PR number come from live reads. **Offline refusals** (none writes anything, all exit 1): no event path; a payload `ReadCommentRef` rejects; `InitTrust` (needs `GITHUB_REPOSITORY`); `InitSCM` (`scmstages.ErrNoCredential` without `GITHUB_TOKEN`/`GH_TOKEN`); `LoadComment` refusing a payload whose claimed repository differs (case-insensitively) from the trusted one.

**Live reads, in flow order** (stage names at `internal/flows/clearance/clearance.go:60-64`): `load-comment` → `forge.Client.IssueComment` (`calls.go:233`: the comment, then the issue endpoint — the only way to tell a PR from a plain issue), so even "not a pull request" and "not addressed to lydite" are decided only after two network reads; then `resolve-head`, `check-permission` and `read-referral` (all `When(addressed)`) before `decide`. Lazily after that: `ChangedPaths` inside `Decide` for `/lydite exempt`; `PullRequestTitle` in the fingerprint stage for `/lydite clear`.

Consequence: a CI end-to-end assertion driven by a synthetic payload alone cannot reach `RenderStatuses`; it needs a loopback API stub reached through `GITHUB_API_URL` (`internal/forge/forge.go:57`), which the in-process tests use via `fakeForge`'s `httptest` server (`TestARenderedClearanceAlsoRendersTheResolvedReferral`). A stub for `.github/workflows/` is tracked as lydite/lydite#236 and not adopted. The stage order and endpoint pairing were carried from the explorer and only the stage names and function locations re-checked here, hence `suspect`.
