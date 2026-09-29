---
name: a-clearance-resolves-a-pending-referral-on-both-routes
kind: invariant
description: A clearance records lydite/clearance then lydite/referral=success on both routes; the relay route admits the referral write narrowly, gated on its own live read of a pending referral.
anchors:
  - path: source/cli/internal/stages/clearance/statuses.go
    blob: efba21b0bb0c
  - path: source/cli/internal/flows/clearance/clearance.go
    blob: f485bdb9be4f
  - path: source/cli/internal/clearance/decide.go
    blob: 2aa8213e1976
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: cccaa62747de
confidence: verified
---

- **Direct route.** `clearancestages.RecordStatuses` (`internal/stages/clearance/statuses.go`) posts `lydite/clearance` and then `lydite/referral` = success on the same head, clearance first, and never attempts the second after the first fails — a partial failure leaves a clearance record beside a standing referral, never a green referral with no record of who cleared it. It runs only when `--status-out` is empty (`Unless(InputRender)` in `internal/flows/clearance/clearance.go`).
- **Rendered route.** `RenderStatuses` writes the same two statuses as documents: `lydite/clearance` at `--status-out`, `lydite/referral` at the `.referral` sibling, clearance first, both from one `statuses(...)` helper.
- **The relay can carry the referral document.** `referralResolution` in `source/cloud-services/pr-relay/src/index.ts` (used at lines ~370 and ~626) admits `lydite/referral` = `success` from a clearance run (`clearanceRun`'s whole condition: a `job_workflow_ref` on the clearance allowlist and not the referral one, an `issue_comment` event, a `refs/heads/` ref), gated on the relay's own live read (`standingStatus`, `index.ts:1250`) that the standing referral is `pending`. Never `failure`/`error`, never trusted from the body. One `/status` request writes one context; ordering is the caller's.
- **TOCTOU.** The relay's read and write are separate requests, so a referral re-run landing `failure` between them is not caught. The CLI route has the same shape, wider: it reads the referral in the `read-referral` stage and `clearance.Decide` returns `KindClear` only for a `pending` one (`internal/clearance/decide.go`), but the post comes stages later, after Fingerprint.

**A stale comment:** `RenderStatuses`'s doc in `statuses.go` (lines ~67-68) still says the relay admits a clearance ref to `lydite/clearance` alone and the referral document is "never relayed" — contradicted by `referralResolution`.

Evidence: `TestClearPostsTheClearanceThenResolvesTheReferralOnTheHead` and `TestARenderedClearanceAlsoRendersTheResolvedReferral` in `cmd/lydite/clearance_test.go`; the `describe("a clearance resolving a pending referral", ...)` block in `pr-relay/src/index.test.ts`.
