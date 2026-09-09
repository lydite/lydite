---
name: fold-unions-findings-before-the-duplicate-shard-check
kind: gotcha
description: A fold appends every shard's findings before the duplicate-row check runs, so two shards reporting one component duplicate its findings in the folded document.
anchors:
  - path: source/cli/cmd/lydite/fold.go
    blob: 039594b726a8
  - path: source/cli/internal/ui/report.go
    blob: 19e2fd50785a
confidence: verified
---

`readShards` (`fold.go:55`) calls `rep.AddFindings(doc.Findings...)` (`fold.go:67`)
unconditionally for every shard document it can read, in its first pass.
`ui.Report.AddFindings` (`internal/ui/report.go:66`) is a bare `append` with no
fingerprint dedup.

The duplicate-shard problem is caught only at the **row** level, in `componentRows`
(`fold.go:254`), which runs afterwards over `in.doc.Rows` and turns two shards
reporting one component into a failing `shards` row. It never touches `rep.findings`.
So in that scenario — a shard-name collision, or a re-run job that was not superseded —
the folded document carries that component's findings twice by the time the run is
failed.

Contained today only because the verdict goes to `StatusFail`, so a mis-sharded run is
never a silent green pass. But nothing corrects or flags the findings array itself: a
consumer reading `findings` off a failed-but-published document, or a future fold that
resumes from a partial set of shards, sees doubled claims with no signal. Anything
built on finding counts or per-finding history should not assume the fold deduplicates.
