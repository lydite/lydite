---
name: ledger-records-closure-runs-once-per-push-attempt
kind: invariant
description: ledgerstages.ComposeRecords returns a gitstate.Records closure rather than records, and only gitstate.Write's retry loop calls it, once per attempt against the branch that attempt fetched.
anchors:
  - path: source/cli/internal/stages/ledger/compose.go
    blob: 63b9e05ca796
  - path: source/cli/internal/stages/ledger/write.go
    blob: 478435f9db65
  - path: source/cli/internal/gitstate/gitstate.go
    blob: 1b2df596613f
confidence: verified
---

`ComposeRecordsOut.Records` (`internal/stages/ledger/compose.go:41`) is a `gitstate.Records` — a function of the attempt's worktree — not a slice. The record flow binds it into `WriteState` (`internal/flows/record/record.go`, `With("Records", flow.FromStage(StageComposeRecords, "Records"))`), which passes it to `gitstate.Write` (`internal/stages/ledger/write.go:49`), the only production caller. `Write` re-fetches the state branch on each of its `attempts` (3, `gitstate.go:755`) and calls the closure against that attempt's worktree. Inside it, `ledger.BranchState(worktree, branch, head.At)` (`compose.go:98`) supplies the open-finding set and previous record, and `gapBefore` (called at `:106`, defined at `:192`) decides whether a gap record is due. Both are questions about the branch as *this attempt* fetched it; computing them once up front would let a retry declare a gap or a resolution a concurrent recording had just filled. See [[gitstate-write-retry-cap-is-a-tolerance-not-a-correctness-bound]] and [[ledger-branchstate-replay-bounded-and-timestamp-ordered]].

**The captured ctx.** `gapBefore(ctx, ...)` is called with the `ctx` `ComposeRecords` received, since `Records` has no ctx parameter. That is sound only while every stage of a run shares one context, as `flow.Run` (`internal/flow/run.go`) hands them. A flow running stages under per-stage derived contexts cancelled when the stage returns would give the closure an already-cancelled ctx by the time `WriteState` invokes it.

A stage `Out` may carry a function only when the question depends on state read fresh at call time and the flow guarantees one call site (ADR 0069).
