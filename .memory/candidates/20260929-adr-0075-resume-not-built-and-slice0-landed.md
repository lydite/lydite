---
about: ADR 0075's resume half is built (state, fingerprint, --fresh, --state-dir, reused counts); its deadline half (--deadline, incomplete, exit 3) is not; the plan's slice 0 (-timeout kept out of go build) has landed
saw:
  - docs/adr/0075-a-mutation-run-resumes-and-stops-at-a-deadline.md
  - agentic/plans/pr22-mutation-resumes.md
  - source/cli/internal/runner/runner.go
  - .lydite/components.yml
  - source/cli/internal/mutation/state.go
  - source/cli/internal/stages/mutation/run.go
---

Checked 2026-09-29, after the resume slice landed.

- Built: `internal/mutation/state.go` (`State`, `MutantID`, `Baseline`), `Options.Known`/`Options.Record` and `Result.CutShort` in `executor.go`, `stateFingerprint` and `componentState` in `stages/mutation/run.go`, `--state-dir`/`--fresh`/`LYDITE_MUTATION_STATE` in `cmd/lydite/mutation.go`.
- Not built: `--deadline`, `KindIncomplete`, exit code 3, and the `lydite/actions` cache wiring. `grep -rn deadline` over `cmd/lydite/mutation.go`, `internal/mutation`, `internal/stages/mutation` finds no implementation.
- Slice 0 is done: `runner.go` `goBuildArgs` = `dropFlags(args, true, goBuildRejected)`, and `.lydite/components.yml` declares `-timeout 30m` on cli. `go-buildonly-passes-go-test-only-flags-to-go-build` is now-false; `cli-suite-outruns-gos-default-test-timeout` is now-false (timeout declared; the baseline's pass was not re-run to confirm).
- `agentic/references/mutation.md` ("no runtime budget ... dies as a CI job timeout") and ADR 0027's "No runtime budget" still stand until the deadline slice reverses them.
