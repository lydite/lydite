---
name: ledger-branchstate-replay-bounded-and-timestamp-ordered
kind: invariant
about: source/cli/internal/ledger/ledger.go
description: ledger.BranchState (and OpenFindings before it) replays a branch's finding transitions in the records' own At order, not file order, and only looks back lookbackMonths (12) — a fingerprint open before that window re-reports as newly appeared rather than being found by an unbounded walk.
anchors:
  - path: source/cli/internal/ledger/ledger.go
    blob: 3b27fa20ca3d2a84ea72ed010fafcfccc1145405
  - path: source/cli/cmd/lydite/record.go
    blob: ebdc162be991acddf6e42ea8a6dd4f05cc400ee2
confidence: verified
---

`ledger.BranchState` (added this session, replacing separate `Latest`/`OpenFindings`
calls to avoid reading the same partitions twice per write attempt) collects every
`KindEntry` record for a branch across `lookbackMonths` (12) months of partitions, then
sorts by the record's own `At` field (`sort.SliceStable`) before replaying
`FindingAppeared`/`FindingResolved` events — never by the order records happen to sit in
a partition file, because two recordings can land out of order (a retried CI job, or
gitstate's own out-of-order note on `Latest`).

The `before` parameter is exclusive: a record whose `At` equals `before` is excluded
from the finding-transition replay (though *included*, at `<=`, for the separate
"newest previous record" answer `BranchState` also returns, matching `Latest`'s own
"at or before" semantics — the two reductions deliberately keep different boundary
rules over the same collected records). This is what lets `cmd/lydite/record.go`'s
`historyRecords` pass the new commit's own `head.At` as `before` without ever reading
that commit's own not-yet-appended events back as history.

The 12-month bound means a fingerprint that has been open longer than that with no
transition recorded in the window re-reports as `FindingAppeared` on the next recording
— accepted deliberately (`OpenFindings`'s doc comment: "a branch with nothing in that
window is adopting finding history, not resuming one worth reconstructing further
back"), the same reasoning `Latest`'s own `lookbackMonths` bound already rests on for
gap detection.
