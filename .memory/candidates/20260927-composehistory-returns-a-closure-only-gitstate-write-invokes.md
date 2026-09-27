---
about: recordstages.ComposeHistory returns a gitstate.Records closure rather than records, and only gitstate.Write's retry loop invokes it (once per attempt, against the branch that attempt fetched); the closure captures the stage's ctx, which is safe only because flow.Run hands one ctx to every stage in the run
saw:
  - source/cli/internal/stages/record/history.go
  - source/cli/internal/stages/record/write.go
  - source/cli/internal/flows/record/record.go
  - source/cli/internal/gitstate/gitstate.go
  - source/cli/internal/flow/run.go
  - docs/adr/0069-a-recordings-history-is-a-deferred-closure-and-its-inputs-cross-a-boundary-type.md
---

`ComposeHistoryOut.Records` (`internal/stages/record/history.go`) is a `gitstate.Records` —
`func(worktree string) ([]ledger.Record, error)` (`internal/gitstate/gitstate.go`) — not a slice.
`recordflow` binds it into `WriteState`'s `Records` (`internal/flows/record/record.go`), and
`WriteState` (`internal/stages/record/write.go`) passes it straight to `gitstate.Write`, the
function's only caller. `Write` loops `attempts` (3) times, re-fetching the state branch each
time, and calls `records(tmp)` against that attempt's worktree. Inside the closure,
`ledger.BranchState(worktree, branch, head.At)` supplies the open-finding set and the previous
record, `findingEvents` diffs against the former, and `gapBefore` decides on the latter — both are
questions about the branch *as this attempt fetched it*; computing them once up front would let a
retry declare a gap or a resolution a concurrent recording had just filled (ADR 0069, and the
function's own doc: "Nothing but the write ever calls it"). Each attempt copies the captured
`entry` (`rec := entry`) so one attempt's events never carry into the next.

What is computed eagerly vs. deferred: branch resolution, `historyComponents`, the no-scalar
check, `gitstate.DescribeCommit(ctx, in.Dir, "HEAD")` and `findingScope` all run inside the stage;
only the branch-dependent parts run in the closure.

**The captured ctx.** The closure calls `gapBefore(ctx, in.Dir, ...)` with the `ctx`
`ComposeHistory` received, not one `gitstate.Write` passes in (`Records` has no ctx parameter).
That is sound here because `flow.Run` (`internal/flow/run.go`) calls every stage with the same
`ctx` it was given, so the write stage's ctx and the captured one are the same value, cancelled
together. A future flow that ran stages under per-stage derived contexts (e.g. a per-stage
timeout that is cancelled when the stage returns) would hand the closure an already-cancelled ctx
by the time `WriteState` invokes it — `ComposeHistory` would have to stop capturing ctx, or
`gitstate.Records` would have to take one.

ADR 0069 limits this shape: a stage `Out` may carry a function only when the question depends on
state read fresh at call time and the flow guarantees exactly one call site. A grep for
`gitstate.Write(` outside `_test.go` finds only `write.go` — that single-caller property is what
`WriteState`'s doc says the invariant rests on.
