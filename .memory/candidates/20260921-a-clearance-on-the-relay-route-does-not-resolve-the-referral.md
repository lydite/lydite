---
about: a clearance posts its own lydite/clearance status, and only the direct-post route also flips lydite/referral to success — the relay route cannot, because a clearance ref may post lydite/clearance only
saw:
  - source/cli/cmd/lydite/status.go
  - source/cli/internal/clearance/decide.go
  - source/cloud-services/pr-relay/src/index.ts
---

- `recordClearance` in `cmd/lydite/status.go` has two routes. With `--status-out` it writes the one `lydite/clearance` document and posts nothing. Without it, it posts `lydite/clearance` and then `lydite/referral` = success on the same head, clearance first so a failure between the two never leaves a green referral with no record of who cleared it.
- The rendered route cannot carry a `lydite/referral` document: the relay admits a clearance workflow ref to `lydite/clearance` only. A repository on that route that requires `lydite/referral` stays blocked after `/lydite clear` until its gate reads `lydite/clearance`; `clearance.Decide` also reads only the referral status, so a repeated `/lydite clear` clears again rather than answering already-passing.
- Found in a panel review: the first cut posted only `lydite/clearance` on the direct route too, which blocked every repository that had not adopted the reusable workflows.

Evidence: `TestClearPostsTheClearanceThenResolvesTheReferralOnTheHead` in `cmd/lydite/clearance_test.go`.
