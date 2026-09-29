---
about: ADR 0075's resume half and its deadline half (--deadline, KindIncomplete, exit 3, the incomplete fold) are both built; only the lydite/actions cache wiring is not; the plan's slice 0 (-timeout kept out of go build) has landed
saw:
  - docs/adr/0075-a-mutation-run-resumes-and-stops-at-a-deadline.md
  - agentic/plans/pr22-mutation-resumes.md
  - source/cli/internal/runner/runner.go
  - .lydite/components.yml
  - source/cli/internal/mutation/state.go
  - source/cli/internal/stages/mutation/run.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/internal/mutation/counts.go
---

Checked 2026-09-29, after the deadline slice landed on its branch.

- Built: `internal/mutation/state.go` (`State`, `MutantID`, `Baseline`), `Options.Known`/`Options.Record` and `Result.CutShort` in `executor.go`, `stateFingerprint` and `componentState` in `stages/mutation/run.go`, `--state-dir`/`--fresh`/`LYDITE_MUTATION_STATE` in `cmd/lydite/mutation.go`.
- Built: `--deadline` (`deadlineAt(processStart, d)` in `cmd/lydite/mutation.go`, `ErrDeadline` and `RunMutantsOut.DeadlineReached` in `stages/mutation/run.go`), `KindIncomplete` with `ComponentOutcome.Measured/Wanted`, `ui.Report.MarkIncomplete` and `ui.ExitIncomplete` (3), and `CountsDocument.IncompleteComponents` in `internal/mutation/counts.go`, which `mutation merge` folds as incomplete-wins.
- Not built: the `lydite/actions` cache wiring (slice 3, in another repository).
- Slice 0 is done: `runner.go` `goBuildArgs` = `dropFlags(args, true, goBuildRejected)`, and `.lydite/components.yml` declares `-timeout 30m` on cli. `go-buildonly-passes-go-test-only-flags-to-go-build` is now-false; `cli-suite-outruns-gos-default-test-timeout` is now-false (timeout declared; the baseline's pass was not re-run to confirm).
- `agentic/references/mutation.md`'s "no runtime budget" section now says a deadline is not a budget, and ADR 0027 carries a pointer to ADR 0075 revising its "No runtime budget" and whole-repository positions.
