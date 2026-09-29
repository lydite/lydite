---
about: what of the pr22 plan's line pointers and wiring still differs from the tree once the resume slice has landed
saw:
  - agentic/plans/pr22-mutation-resumes.md
  - source/cli/internal/stages/mutation/run.go
  - source/cli/internal/stages/mutation/declaration.go
  - source/cli/internal/mutation/executor.go
  - source/cli/internal/flows/mutation/mutation.go
---

Checked 2026-09-29, after the resume slice landed.

- The plan's line numbers predate the Flow migration; find symbols, not lines. Flow inputs are `Input*` constants plus `Params.Inputs()` in `internal/flows/mutation/mutation.go`.
- The tree digest is computed inside `ScopeChange` (`declaration.go`) from its own `gitdiff.Tracked` listing, made when a worker directory is needed or resume is on; `TreeDigest` is `""` when resume is off or the state cannot be prepared, and `RunMutants` resumes only when both `StateDir` and `TreeDigest` are non-empty.
- `run.go` still comments that ADR 0027 refuses a runtime budget at the `costProjection` write; that stays until `--deadline` lands.
