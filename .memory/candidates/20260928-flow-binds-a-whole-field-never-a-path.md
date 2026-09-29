---
about: flow.FromStage binds one whole exported Out field and has no nested path, so a stage that needs part of the pull request takes the whole forge.PullRequestRef
saw:
  - source/cli/internal/flow/flow.go
  - source/cli/internal/stages/scm/scm.go
  - source/cli/internal/stages/threads/threads.go
  - source/cli/internal/stages/review/compose_status.go
---

- `flow.FromStage(stage, field)` resolves `field` with `exportedField` (`internal/flow/flow.go`) against the earlier stage's `Out` type and checks it with `AssignableTo` against the target `In` field. Nothing walks into a nested struct: `FromStage(StageLoadPullRequest, "Ref.Number")` is not a binding, and binding `"Ref"` to an `int` field fails `Build`.
- `scmstages.LoadPullRequestOut` has exactly one field, `Ref forge.PullRequestRef` (`internal/stages/scm/scm.go`). So every stage reading the pull request's number or head takes `Ref forge.PullRequestRef` whole and reads the part it needs: `reviewstages.ComposeStatus` (`internal/stages/review/compose_status.go`) and every threads stage from `ListThreads` to `Open` (`internal/stages/threads/threads.go`, `apply.go`).
- A stage `In` designed with loose `Number int` / `Head string` fields cannot be wired to `load-pull-request` without either an adapter stage that does no work or an engine change; the threads stages were first written that way and reshaped. ADR 0073's context section records the choice.
