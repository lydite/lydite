---
name: clearance-oidc-ref-is-not-a-pull-ref
kind: invariant
description: A clearance run's OIDC ref is a branch ref, so the relay takes the pull request from the body, and that exemption is safe only because it is keyed on job_workflow_ref plus event_name and ref, never on the allowlist alone.
anchors:
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: cccaa62747de
  - path: source/cloud-services/libs/github-app/src/oidc.ts
    blob: 148f32f2e36f
  - path: .github/workflows/gt-lydite-clearance.yml
    blob: fc210a62bbdc
  - path: source/cli/internal/clearance/decide.go
    blob: 2aa8213e1976
confidence: verified
---

- The clearance workflow (`.github/workflows/gt-lydite-clearance.yml`) triggers on `issue_comment`, so its OIDC `ref` is a `refs/heads/...` ref, never `refs/pull/<n>/...`. The claims cannot name the pull request; `resolveTarget` in `pr-relay/src/index.ts` takes it from the body's `pull_request` for a clearance run only.
- A clearance run is `clearanceRun`'s conjunction (`index.ts:535`): `job_workflow_ref` in the clearance allowlist (and not the referral one), `event_name === "issue_comment"`, and a `refs/heads/` ref. The allowlist alone names a workflow file, and a file says nothing about what triggered it: an allowlisted callee reached from `push`, `workflow_dispatch` or a same-repo `pull_request` would otherwise let whoever can open a PR name any pull request or post `lydite/clearance`. Found in a panel review; `event_name` is on `ActionsClaims` for this.
- `/status` verifies the body's `sha` against the live head (`pullRequest()`), and `/comment` requires the pull request to resolve and be open. `/review` is never exempted.
- The relay accepts the clearance status only as `lydite/clearance`, which is `clearance.ClearanceContext` in `internal/clearance/decide.go`; `clearance.Context` stays `lydite/referral` and is the verdict's context.
- `id-token: write` appears only in `ci-orchestration.yml` and `gt-lydite-clearance.yml` (`grep -n id-token .github/workflows/*.yml`).
