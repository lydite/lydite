---
name: ledger-branchstate-replay-bounded-and-timestamp-ordered
kind: invariant
description: ledger.BranchState replays finding transitions in records' own At order over a 12-month lookback, so a finding open longer than that with no transition re-reports as newly appeared.
anchors:
  - path: source/cli/internal/ledger/ledger.go
    blob: 3276b69cc075
  - path: source/cli/internal/stages/ledger/compose.go
    blob: 63b9e05ca796
confidence: verified
---

`ledger.BranchState` (`internal/ledger/ledger.go`, doc at ~505) is the one implementation of the finding-events replay; an earlier near-duplicate `OpenFindings` was removed after review caught the copies drifting. It collects every `KindEntry` record for a branch across `lookbackMonths` (12, `ledger.go:123`) months of partitions, then sorts by the record's own `At` (`sort.SliceStable`, `ledger.go:566`) before replaying `FindingAppeared`/`FindingResolved` — never by position in a partition file, because two recordings can land out of order (a retried CI job).

The `before` parameter is exclusive for the finding-events replay (a record whose `At` equals `before` is excluded) but inclusive (`<=`) for the "newest previous record" answer it also returns, matching `Latest`'s "at or before". This is what lets `ComposeRecords` (`internal/stages/ledger/compose.go:98`) pass the new commit's own `head.At` as `before` without reading that commit's not-yet-appended events back as history.

The 12-month bound means a fingerprint open longer than that with no transition in the window re-reports as `FindingAppeared` on the next recording — accepted deliberately ("a branch with nothing in that window is adopting finding history, not resuming one worth reconstructing further back"), the same reasoning `Latest`'s bound rests on for gap detection.
