---
about: an interrupted lydite mutation withdraws the scheduled components whose gating row is StatusFail, deciding it off the row outcomeRow(o, false) renders rather than the displayed one, so --no-gate withdraws exactly what a gating run would — a survivor loses its row, findings and mutants.json entry either way, and a completed component with every mutant killed keeps them, differing only in its row's vote
saw:
  - source/cli/cmd/lydite/mutation.go
  - source/cli/cmd/lydite/mutation_test.go
  - source/cli/internal/stages/mutation/run.go
  - source/cli/internal/stages/mutation/record.go
  - docs/adr/0065-a-stage-reports-its-outcome-as-data-and-the-cli-alone-decides-the-rows.md
  - agentic/references/mutation.md
---

`internal/stages/mutation/run.go`'s `RunMutants` states facts only: after `scheduler.Run` it marks
`Scheduled` on every component it handed to the scheduler (line ~250) and sets
`Interrupted = ctx.Err() != nil` (line ~252); every outcome keeps its `Kind` and facts either way.
`RecordMutants` (`record.go` line ~48) records every outcome it is handed whose `Ran()` is true
(`countsOf`, line ~65) and trusts the caller to have removed withdrawn ones — its `Components`
field's comment says only the caller knows which verdicts it withdrew.

The decision is `cmd/lydite/mutation.go`'s. `addMutationRows` (line ~459) builds each displayed
row through `outcomeRow(o, how.noGate)` → `kindRow`, collects the `Scheduled` indices, and on
`Interrupted` builds a second, gating row per component with `outcomeRow(o, false)` (line ~482) and
runs `withdrawInterrupted` (line ~637) over *those* rows and the shared results. `withdrawInterrupted`
replaces a row with the unmeasured "not completed" row and zeroes its `componentMutation` **only
when the row's status is `ui.StatusFail`** — so the gating status decides, and `completedRow`'s
--no-gate conversion of a survivor's Fail into Context never reaches the check. Every scheduled
component left with `!results[i].ran` then takes its gating row: the withdrawn row where it was
withdrawn, and otherwise a kind that never completed, whose row `noGate` does not touch. Only
outcomes whose `componentMutation.ran` survived are returned as `kept` and passed to
`recordMutants` → the `NewRecord` flow. So after an interrupt, gating or not:

- a component that completed with every mutant killed keeps its row (`pass`, or `context` under
  --no-gate) and is written to `mutants.json` — pinned by the "an interrupted run" case of
  `TestAMutationRunsOutcomesRenderAsOneReport` (`mutation_test.go` line ~2065, `wantKept: b`) and
  by `TestAnInterruptedRunUnderNoGateWithdrawsWhatAGatingRunWould` (line ~2136);
- a component with a survivor, or one whose setup failed (`KindBlocked`, scheduled), is
  withdrawn — row, findings and counts — under --no-gate exactly as when gating, pinned by that
  same second test;
- a component that never started stays "not run", and one that could not be planned keeps its
  own row.

The teardown interplay (`teardownFailureReplaces`, line ~712, replaces only Pass and Context):
a gating survivor's Fail is never replaced by a failed teardown, so it is withdrawn; under
--no-gate the displayed row is the teardown's, but the withdrawal reads the gating Fail and the
component renders the gating run's "not completed" row. A kill-everything component with a failed
teardown renders the teardown row under both, since Pass and Context are both replaced. A decided
teardown row whose own status is not Fail therefore never keeps a survivor's claims under
--no-gate either. This matches `agentic/references/mutation.md` (line ~59), which lists "a run
interrupted before it finished" among the rows --no-gate leaves untouched.
