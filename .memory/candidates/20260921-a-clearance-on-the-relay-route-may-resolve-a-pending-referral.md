---
about: a clearance posts its own lydite/clearance status on both routes, and the relay route can also flip a pending lydite/referral to success — narrowly, via a live read, not by trusting the caller
saw:
  - source/cli/cmd/lydite/status.go
  - source/cli/internal/clearance/decide.go
  - source/cloud-services/pr-relay/src/index.ts
---

- `recordClearance` in `cmd/lydite/status.go` has two routes. With `--status-out` it writes the one `lydite/clearance` document and posts nothing. Without it, it posts `lydite/clearance` and then `lydite/referral` = success on the same head, unconditionally, clearance first so a failure between the two never leaves a green referral with no record of who cleared it.
- The rendered route can now also carry a `lydite/referral` document: `referralResolution`/`standingStatus` in `source/cloud-services/pr-relay/src/index.ts` (`standingStatus` was `currentStatus` before ADR 0053's `/merge-group` route needed the whole status entry, not just its state) let a clearance-authority ref (allowlisted `CLEARANCE_WORKFLOW_REFS` ref, `event_name: issue_comment`, `refs/heads/` ref) move `lydite/referral` from `pending` to `success`, gated on the relay's own live read of the standing status with the installation token — never `failure`/`error`, never trusted from the request body. One `/status` request writes one context; the relay enforces no ordering between the two writes, so a caller achieves "clearance first" the same way the direct route does, by sequencing its own two requests.
- The relay's read-then-write has a narrow, accepted TOCTOU window (a referral re-run landing `failure` on the same head between the relay's read and its write is not caught) — still stricter than the direct-post route, which does no live read at all before flipping `lydite/referral`.
- Found in a panel review: the first cut posted only `lydite/clearance` on the direct route too, which blocked every repository that had not adopted the reusable workflows.

Evidence: `TestClearPostsTheClearanceThenResolvesTheReferralOnTheHead` in `cmd/lydite/clearance_test.go`; the `describe("a clearance resolving a pending referral", ...)` block in `source/cloud-services/pr-relay/src/index.test.ts`.
