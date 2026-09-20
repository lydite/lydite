---
name: mutatecomponent-teardown-only-replaces-a-pass-or-context-row
kind: invariant
about: source/cli/cmd/lydite/mutation.go
description: mutateComponent's deferred teardown check replaces the row only when the measurement itself would otherwise report success (StatusPass or, under --no-gate, StatusContext) — a row already reporting a survivor or a setup failure keeps that reason instead.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 02b5149b468c00483a1f030a1e4aca19419891d3
confidence: verified
---

`mutateComponent` defers a teardown run and, if it fails, replaces `row` with the failure —
but only when `teardownFailureReplaces(row.Status)` holds, i.e. the row is currently
`StatusPass` or `StatusContext`. A survivor's `StatusFail` row is left alone even if teardown
also fails: the cause a reader acts on is the survivor, which is upstream of the teardown.

Before `--no-gate` (ADR 0048) existed, this check was just `row.Status == ui.StatusPass`,
because a passing component was the only completed-and-clean state reachable at that point —
`StatusContext` from `mutation: false` returns earlier, before the defer is even installed.
Once `--no-gate` converts a clean *and* a survivor row's `StatusPass`/`StatusFail` to
`StatusContext` before the defers run, a `StatusPass`-only check would have made a failing
teardown under `--no-gate` invisible rather than merely non-voting — the row would keep
reporting `StatusContext` with no trace the teardown ever failed.

Anyone adding another status that can mean "the measurement itself found nothing wrong" must
check `teardownFailureReplaces` (or wherever this predicate lives) is widened to include it,
or a real teardown failure will be silently absorbed into a green-looking row.
