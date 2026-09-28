---
name: gitstate-write-retry-cap-is-a-tolerance-not-a-correctness-bound
kind: invariant
about: source/cli/internal/gitstate/gitstate.go
description: gitstate.Write's attempts=3 retry loop tolerates exactly 3 concurrent writers to the lydite state branch; a writer that loses the race on every attempt returns an error rather than a silent success, so exhaustion is reported and the next successful write records the gap explicitly.
anchors:
  - path: source/cli/internal/gitstate/gitstate.go
    blob: 1b2df596613f5a54dfc13fc50edb4e55957bba0a
  - path: source/cli/internal/gitstate/gitstate_test.go
    blob: 55d6eb8f358e88aeaf6f1a0bccd74d6cb64e770c
  - path: source/cli/internal/stages/record/write.go
    blob: 0e19723808c88ee0c2a752185e0a49d2697e2ebb
  - path: source/cli/internal/stages/record/history.go
    blob: e3d49b04c9dcba55acdcfb3a1f2914b48bedae6b
confidence: verified
---

`Write`'s retry loop re-fetches `origin lydite` and re-appends against the fresh tip on every
attempt, so a push is rejected only when another writer landed between this attempt's fetch and
its own push. Each concurrent writer lands exactly once, so a writer racing N−1 others is
rejected at most N−1 times — `attempts = 3` therefore tolerates exactly 3 writers landing at
once, proven by `TestThreeOverlappingRunsAllRecord`.

Past the cap, nothing is silently lost: `Write` returns a wrapped error naming the branch and
attempt count, `landed` is nil, and no partial state reaches the branch (proven by
`TestARunThatLosesEveryRaceRecordsNothingAndSaysSo`). `Write`'s one production caller is the
record flow's `WriteState` stage (`internal/stages/record/write.go`), which returns the error
unchanged; `recordflow` wires that stage `.OnError(flow.RecordAndContinue)`, and
`cmd/lydite/record.go`'s `recordRows` renders it — a failing `record` row when a baseline was being
landed, and only an amber `history` row when the write carried history alone. The *next*
successful `Write` reads the branch's own newest record, finds it is not the new commit's parent,
and appends an explicit `ledger.KindGap` record.
The retry cap therefore bounds how much overlap one writer survives, not whether the ledger's
completeness guarantee holds — raising the cap only makes a gap rarer, never falser.

`gapBefore` (`internal/stages/record/history.go`, called from inside the `gitstate.Records`
closure `ComposeHistory` returns, so once per write attempt) does not call `ledger.Latest` for
this — it reads `ledger.BranchState`'s `previous`/`hasPrevious` return instead, which computes the same "newest previous record" answer as one part of a single
partition walk shared with the finding-diff replay (`BranchState`'s own doc comment explains
why: `gitstate.Write` retries its closure up to three times, and two separate walks per attempt
would parse a year of partitions as many as six times per commit). `ledger.Latest` itself
still exists, unchanged, and is exercised directly by `internal/gitstate/gitstate_test.go` — it
is not dead code, just no longer this call site's path to the same answer.
