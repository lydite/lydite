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
confidence: verified
---

`Write`'s retry loop re-fetches `origin lydite` and re-appends against the fresh tip on every
attempt, so a push is rejected only when another writer landed between this attempt's fetch and
its own push. Each concurrent writer lands exactly once, so a writer racing N−1 others is
rejected at most N−1 times — `attempts = 3` therefore tolerates exactly 3 writers landing at
once, proven by `TestThreeOverlappingRunsAllRecord`.

Past the cap, nothing is silently lost: `Write` returns a wrapped error naming the branch and
attempt count, `landed` is nil, and no partial state reaches the branch (proven by
`TestARunThatLosesEveryRaceRecordsNothingAndSaysSo`). The caller (`cmd/lydite/record.go`) turns
that error into a failing row, and the *next* successful `Write` reads the branch's own newest
record, finds it is not the new commit's parent, and appends an explicit `ledger.KindGap` record.
The retry cap therefore bounds how much overlap one writer survives, not whether the ledger's
completeness guarantee holds — raising the cap only makes a gap rarer, never falser.

As of the finding-transitions slice (ADR 0058), `record.go`'s `gapBefore` no longer calls
`ledger.Latest` directly for this — it reads `ledger.BranchState`'s `previous`/`hasPrevious`
return instead, which computes the same "newest previous record" answer as one part of a single
partition walk shared with the finding-diff replay (`BranchState`'s own doc comment explains
why: `gapBefore`'s `Latest` lookup and `OpenFindings` were each reading the same
`lookbackMonths` of partitions independently, once per write attempt). `ledger.Latest` itself
still exists, unchanged, and is exercised directly by `internal/gitstate/gitstate_test.go` — it
is not dead code, just no longer this call site's path to the same answer.
