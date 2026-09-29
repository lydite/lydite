---
name: interrupted-mutation-run-withdraws-only-failing-rows
kind: invariant
description: An interrupted lydite mutation withdraws scheduled components whose gating row is StatusFail, decided off the gating row rather than the displayed one, so --no-gate withdraws exactly what a gating run would.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 3ad21f631005
  - path: source/cli/cmd/lydite/mutation_test.go
    blob: 17a556c497c0
  - path: source/cli/internal/stages/mutation/run.go
    blob: f2d6f052cb32
  - path: source/cli/internal/stages/mutation/record.go
    blob: c0d79cc45da4
confidence: suspect
---

`RunMutants` (`internal/stages/mutation/run.go`) states facts only: after `scheduler.Run` it marks `Scheduled` on every component handed to the scheduler and sets `Interrupted = ctx.Err() != nil`. `RecordMutants` (`record.go`) records every outcome it is handed whose `Ran()` is true and trusts the caller to have removed withdrawn ones.

The decision is `cmd/lydite/mutation.go`'s. `addMutationRows` builds each displayed row through `outcomeRow(o, how.noGate)` (`:541`); on `Interrupted` it builds a second, *gating* row per component with `outcomeRow(o, false)` and runs `withdrawInterrupted` (`:637`) over those rows and the shared results. `withdrawInterrupted` replaces a row with the unmeasured "not completed" row and zeroes its `componentMutation` **only when the row's status is `ui.StatusFail`** — the gating status decides, so `completedRow`'s (`:690`) `--no-gate` conversion of a survivor's Fail into Context never reaches the check. So after an interrupt, gating or not:

- a component that completed with every mutant killed keeps its row (`pass`, or `context` under `--no-gate`) and its `mutants.json` entry (`TestAMutationRunsOutcomesRenderAsOneReport` "an interrupted run" case, and `TestAnInterruptedRunUnderNoGateWithdrawsWhatAGatingRunWould`);
- a component with a survivor, or whose setup failed (`KindBlocked`), is withdrawn — row, findings and counts — under `--no-gate` exactly as when gating;
- a component that never started stays "not run"; one that could not be planned keeps its own row.

Teardown interplay: `teardownFailureReplaces` (`:712`) replaces only Pass/Context, so a gating survivor's Fail is withdrawn on its own; a kill-everything component whose teardown failed gets a Fail teardown row and is withdrawn too, with or without `--no-gate`. Matches `agentic/references/mutation.md`, which lists "a run interrupted before it finished" among rows `--no-gate` leaves untouched. Function locations re-checked; the per-case behaviour is from the explorer's reading, hence `suspect`.
