---
about: a clearance run's OIDC ref is the default branch, not refs/pull/N, so the relay takes its pull request from the body — and that exemption is safe only because it is keyed on job_workflow_ref plus event_name and ref, never on the allowlist alone
saw:
  - source/cloud-services/pr-relay/src/index.ts
  - source/cloud-services/libs/github-app/src/oidc.ts
  - .github/workflows/lydite-clearance.yml
  - .github/workflows/lydite-pr.yml
---

- `lydite-clearance.yml` triggers on `issue_comment`, so its OIDC `ref` is a `refs/heads/...` ref and never `refs/pull/<n>/...`. The claims cannot name the pull request; `resolveTarget` in `pr-relay/src/index.ts` takes it from the body's `pull_request` for a clearance run only.
- A clearance run is `clearanceRun`'s conjunction: `job_workflow_ref` in `CLEARANCE_WORKFLOW_REFS` (and not `REFERRAL_WORKFLOW_REFS`), `event_name === "issue_comment"`, and a `refs/heads/` ref. The allowlist alone names a workflow file, and a file says nothing about what triggered it: an allowlisted callee reached from `push`, `workflow_dispatch` or a same-repo `pull_request` would otherwise let whoever can open a PR name any pull request or post `lydite/clearance`. Found in a panel review; `event_name` is on `ActionsClaims` for this.
- `/status` verifies the body's `sha` against the live head (`pullRequest()`), and `/comment` requires the pull request to resolve and be open. `/review` is never exempted.
- The relay accepts the clearance status only as `lydite/clearance`, which is `clearance.ClearanceContext` in `source/cli/internal/clearance/decide.go`; `clearance.Context` stays `lydite/referral` and is the verdict's context.
- Only `publish` in `lydite-pr.yml` holds `id-token: write`; `referral-publish` holds `statuses: write` and no `id-token`, so it cannot mint an OIDC token today.

Evidence: `grep -n id-token .github/workflows/*.yml` shows one hit in `lydite-pr.yml`.
