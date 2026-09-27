---
about: a clearance records lydite/clearance then lydite/referral=success on both routes; the relay route admits the referral write narrowly, via its own live read, not by trusting the caller
saw:
  - source/cli/internal/stages/clearance/statuses.go
  - source/cli/internal/flows/clearance/clearance.go
  - source/cli/internal/clearance/decide.go
  - source/cloud-services/pr-relay/src/index.ts
  - source/cli/cmd/lydite/clearance_test.go
  - source/cli/internal/stages/clearance/statuses_test.go
---

Re-checked on `refactor/flow-architecture-clearance-pilot`: `recordClearance` in
`cmd/lydite/status.go` was deleted and split into two clearance-flow stages. The behaviour holds;
the pointers moved, and two comments elsewhere now misdescribe it (below).

- **Direct route.** `clearancestages.RecordStatuses` (`internal/stages/clearance/statuses.go`)
  posts `lydite/clearance` and then `lydite/referral` = success on the same head, clearance first,
  and never attempts the second after the first fails — so a partial failure leaves a clearance
  record beside a standing referral, never a green referral with no record of who cleared it.
  It runs only when `--status-out` is empty (`Unless(InputRender)` in
  `internal/flows/clearance/clearance.go`).
- **Rendered route.** `clearancestages.RenderStatuses` writes the same two statuses as documents:
  `lydite/clearance` at `--status-out`, `lydite/referral` at the `.referral` sibling
  (`referralDocument`), clearance first. Both derive from one `statuses(...)` helper.
- **The relay can carry the referral document.** `referralResolution` in
  `source/cloud-services/pr-relay/src/index.ts` admits `lydite/referral` = `success` from a
  clearance run (a `job_workflow_ref` on `CLEARANCE_WORKFLOW_REFS` and not also on
  `REFERRAL_WORKFLOW_REFS`, an `issue_comment` event, a `refs/heads/` ref — `clearanceRun`'s
  whole condition), gated on the relay's own live read via `standingStatus`
  with the installation token that the standing referral is `pending`. Never `failure`/`error`,
  never trusted from the body. One `/status` request writes one context; ordering is the caller's.
- **TOCTOU.** The relay's read and write are separate requests, so a referral re-run landing
  `failure` between them is not caught. The CLI route has the same shape, wider: it reads the
  standing referral in the `read-referral` stage (`scmstages.ReadStatus`) and `clearance.Decide`
  only returns `KindClear` for a `pending` one (`internal/clearance/decide.go`), but the post comes
  stages later, after Fingerprint (which may run a comparison).

**Two comments now misdescribe this.** `statuses.go`'s `RenderStatuses` doc (carried over from
the deleted `status.go`) says "the relay admits a clearance ref to `lydite/clearance` alone: the
referral document is ... never relayed" — contradicted by `referralResolution`. And
`index.ts`'s comment above the `standingStatus` read still names "`recordClearance` in
`cmd/lydite/status.go`", which no longer exists, and says that route does "no live read at all"
— imprecise, since the CLI does read the status before deciding, just not immediately before
the write.

Evidence: `TestClearPostsTheClearanceThenResolvesTheReferralOnTheHead` and
`TestARenderedClearanceAlsoRendersTheResolvedReferral` in `cmd/lydite/clearance_test.go`;
`TestRecordStatusesPostsTheClearanceThenTheResolvedReferral` in
`internal/stages/clearance/statuses_test.go`; the `describe("a clearance resolving a pending
referral", ...)` block in `source/cloud-services/pr-relay/src/index.test.ts`.
