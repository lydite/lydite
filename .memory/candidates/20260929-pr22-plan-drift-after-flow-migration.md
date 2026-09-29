---
about: The pr22 plan's slice 1/2 line pointers and wiring predate the Flow migration; where they now differ
saw:
  - agentic/plans/pr22-mutation-resumes.md
  - source/cli/internal/stages/mutation/run.go
  - source/cli/internal/stages/mutation/declaration.go
  - source/cli/internal/mutation/executor.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/internal/flows/mutation/mutation.go
---

Checked 2026-09-29 by reading the plan against the tree.

- `mutateComponent` is `run.go:359`, not 358. Order now: `prepareTarget`, `Lifecycle.ClearReport`, `Prepare`, `StartServices`, teardown defer, setup `RunCommands`, baseline under `slots.Run` (`:396-403`), `memoryBudget`, `coverage.Measure` (`:422`), `generate`, `costProjection`, `mutation.Execute` (`:445`). Prepare/services/setup are `:374-388`, not 370-389. A baseline cache hit skips the baseline run and Measure, but Measure supplies `report.Executed`, which `generate` needs, so the baseline record must carry the executed lines.
- `out.Interrupted = ctx.Err() != nil` is `run.go:253` (plan: 252), set once in `RunMutants` after the scheduler returns; a deadline needs `context.Cause` there and in `mutateComponent`.
- `Execute` is `executor.go:194`; the "ended before this mutant was built" placeholder is `:202`, cutShort placeholders `:299-303` and `:341-343` (plan: 290-292).
- `ScopeChange` (`declaration.go:233`) reads `gitdiff.Tracked` only when `needsWorktree` (Rust/TypeScript/Python components). A Go-only repo gets `Files == nil`, so the plan's "digest computed once in ScopeChange from the one list" has no list for Go; the digest must fetch the list itself unconditionally, while the state-dir exclusion still applies to `mutation.Tree`'s list.
- Flow inputs are `Input*` constants plus `Params.Inputs()` in `internal/flows/mutation/mutation.go`; the CLI builds `Params` at `cmd/lydite/mutation.go:128-150`.
- `run.go` still comments "ADR 0027 refuses a runtime budget ... nothing here stops a run" at the `costProjection` write; false once `--deadline` lands.
- `docs/adr/` holds three files numbered 0075; the plan links the mutation one by full filename.
