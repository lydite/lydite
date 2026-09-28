---
about: an interrupted lydite mutation withdraws only the scheduled components whose row is StatusFail; a completed component whose row is Pass — or, under --no-gate, Context, survivors included — keeps its row, its findings and its entry in mutants.json, because the withdrawal keys on the row's status after completedRow has already run
saw:
  - source/cli/cmd/lydite/mutation.go
  - source/cli/cmd/lydite/mutation_test.go
  - source/cli/internal/stages/mutation/run.go
  - source/cli/internal/stages/mutation/record.go
  - docs/adr/0065-a-stage-reports-its-outcome-as-data-and-the-cli-alone-decides-the-rows.md
  - agentic/references/mutation.md
---

`internal/stages/mutation/run.go`'s `RunMutants` states facts only: after `scheduler.Run` it marks
`Scheduled` on every component it handed to the scheduler (line ~249) and sets
`Interrupted = ctx.Err() != nil` (line ~252); every outcome keeps its `Kind` and facts either way.
`RecordMutants` (`record.go` line ~48) records every outcome it is handed whose `Ran()` is true
(`countsOf`, line ~65) and trusts the caller to have removed withdrawn ones — its `Components`
field's comment says only the caller knows which verdicts it withdrew.

The decision is `cmd/lydite/mutation.go`'s. `addMutationRows` (line ~453) builds each row through
`outcomeRow` → `kindRow`, collects the `Scheduled` indices, and on `Interrupted` calls
`withdrawInterrupted` (line ~619), which replaces a row with the unmeasured "not completed" row and
zeroes its `componentMutation` **only when `rows[i].Status == ui.StatusFail`**. Only outcomes whose
`componentMutation.ran` survived are returned as `kept` and passed to `recordMutants` → the
`NewRecord` flow. So after an interrupt:

- a component that completed with every mutant killed keeps its `pass` row and is written to
  `mutants.json` — pinned by the "an interrupted run" case of
  `TestAMutationRunsOutcomesRenderAsOneReport` (`mutation_test.go` line ~2061, `wantKept: b`);
- a gating component with a survivor, or one whose setup failed (`KindBlocked`, scheduled), is
  withdrawn — row, findings and counts;
- a component that never started stays "not run", and one that could not be planned keeps its
  own row.

Not pinned by any test, and following from the order of operations: `kindRow`'s `KindCompleted`
arm applies `completedRow(row, noGate)` before `withdrawInterrupted` looks at the status, so under
`--no-gate` a completed component *with survivors* already reads `StatusContext` and is **not**
withdrawn — its survivors stay in the report as findings and its counts are recorded, even though
the withdrawal's own rationale (under cancellation a survivor cannot be told from a killed suite)
applies to it equally. ADR 0065's Consequences say withdrawal depends on the row ("a survivor
withdraws; `KindMutationOff` does not") without stating the Pass/Context cases, and
`agentic/references/mutation.md` (line ~59) lists "a run interrupted before it finished" among
rows `--no-gate` leaves untouched, which describes the "not completed" row but not this path.
