---
name: mutatecomponent-teardown-only-replaces-a-pass-or-context-row
kind: invariant
about: source/cli/cmd/lydite/mutation.go
description: a component's failed teardown replaces its mutation row only when the measurement itself would otherwise report success (StatusPass or, under --no-gate, StatusContext) — a row already reporting a survivor or a setup failure keeps that reason instead. The stage carries the teardown error beside the outcome; the CLI decides whether it takes over the row.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 42d44e167d073f970c70ff265aca2516e1bda35f
  - path: source/cli/internal/stages/mutation/run.go
    blob: f2d6f052cb325cc8b4dadc9c82db0c9748d59f9a
confidence: verified
---

`internal/stages/mutation/run.go`'s `mutateComponent` defers the component's teardown commands
(line ~384) once its services have started, on `context.WithoutCancel(ctx)`, and stores the
Lifecycle's error on the outcome's `TeardownErr` — beside whatever `Kind` the component reached,
never in place of it. The stage decides nothing about rows.

`cmd/lydite/mutation.go`'s `outcomeRow` (line ~523) is where the teardown matters: it builds the
row through `kindRow` first, and replaces it with `lifecycleRow(label, o.TeardownErr)` only when
`teardownFailureReplaces(row.Status)` holds — `StatusPass` or `StatusContext`. A survivor's
`StatusFail` row, or a row naming why the component could not be measured, is left alone even
if teardown also fails: the cause a reader acts on is upstream of the teardown.

The order is load-bearing: `kindRow`'s `KindCompleted` arm applies `completedRow(row, noGate)`
before `outcomeRow` asks the predicate, so under `--no-gate` (ADR 0048) a clean *and* a
survivor row have already become `StatusContext`. A `StatusPass`-only predicate would make a
failing teardown under `--no-gate` invisible — the row would keep reporting `StatusContext` with
no trace the teardown failed. `KindMutationOff`'s own `StatusContext` row is not at risk: that
kind returns from `prepareTarget` before any service starts, so its `TeardownErr` is always nil.

Anyone adding another status that can mean "the measurement itself found nothing wrong" must
widen `teardownFailureReplaces` to include it, or a real teardown failure will be silently
absorbed into a green-looking row.
