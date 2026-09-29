---
name: gitstate-write-retry-cap-is-a-tolerance-not-a-correctness-bound
kind: invariant
description: gitstate.Write's attempts=3 tolerates exactly three overlapping writers; a writer losing every race returns an error, and the next successful write records an explicit gap.
anchors:
  - path: source/cli/internal/gitstate/gitstate.go
    blob: 1b2df596613f
  - path: source/cli/internal/gitstate/gitstate_test.go
    blob: 55d6eb8f358e
  - path: source/cli/internal/stages/ledger/write.go
    blob: 478435f9db65
  - path: source/cli/internal/stages/ledger/compose.go
    blob: 63b9e05ca796
confidence: verified
---

`Write`'s retry loop re-fetches `origin lydite` and re-appends against the fresh tip on every attempt, so a push is rejected only when another writer landed between this attempt's fetch and its push. Each concurrent writer lands once, so a writer racing N−1 others is rejected at most N−1 times: `attempts = 3` (`gitstate.go:755`) tolerates exactly 3 writers at once, proven by `TestThreeOverlappingRunsAllRecord` (`gitstate_test.go:833`).

Past the cap nothing is silently lost: `Write` returns a wrapped error naming the branch and attempt count, `landed` is nil, and no partial state reaches the branch (`TestARunThatLosesEveryRaceRecordsNothingAndSaysSo`, `:877`). `Write`'s one production caller is `WriteState` (`internal/stages/ledger/write.go:49`), which returns the error unchanged; the record flow wires it `.OnError(flow.RecordAndContinue)` and `cmd/lydite/record.go`'s `recordRows` renders it — a failing `record` row when a baseline was being landed, an amber `history` row when the write carried history alone. The *next* successful `Write` reads the branch's newest record, finds it is not the new commit's parent, and appends an explicit `ledger.KindGap` record (`gapBefore`, `internal/stages/ledger/compose.go:192`). So the cap bounds how much overlap one writer survives, not whether the ledger's completeness guarantee holds — raising it only makes a gap rarer, never falser. `gapBefore` takes `BranchState`'s `previous` answer rather than calling `ledger.Latest`, to share one partition walk; see [[ledger-branchstate-replay-bounded-and-timestamp-ordered]].
