---
name: mutation-teardown-replaces-only-a-pass-or-context-row
kind: invariant
description: A failed teardown replaces a mutation row only when the row would otherwise report success (Pass, or Context under --no-gate); the stage carries the error beside the outcome and the CLI decides.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 3ad21f631005
  - path: source/cli/internal/stages/mutation/run.go
    blob: f2d6f052cb32
confidence: verified
---

`mutateComponent` (`internal/stages/mutation/run.go`) defers the component's teardown commands once its services have started (line 385, on `context.WithoutCancel(ctx)`) and stores the Lifecycle's error on the outcome's `TeardownErr` (`run.go:146`) beside whatever `Kind` the component reached, never in place of it. The stage decides nothing about rows.

`outcomeRow` (`cmd/lydite/mutation.go:541`) builds the row through `kindRow` first and replaces it with `lifecycleRow(label, o.TeardownErr)` only when `teardownFailureReplaces(row.Status)` (`:712`) holds — `StatusPass` or `StatusContext`. A survivor's `StatusFail`, or a row naming why the component could not be measured, is left alone even if teardown also fails: the cause a reader acts on is upstream.

The order is load-bearing: `kindRow`'s `KindCompleted` arm applies `completedRow(row, noGate)` (`:690`) before `outcomeRow` asks the predicate, so under `--no-gate` (ADR 0048) a clean and a survivor row have already become `StatusContext`; a `StatusPass`-only predicate would hide a failing teardown. `KindMutationOff`'s `StatusContext` row is safe because that kind returns before any service starts, so `TeardownErr` is nil. Anyone adding a status that can mean "the measurement found nothing wrong" must widen `teardownFailureReplaces`, or a real teardown failure is absorbed into a green-looking row.
