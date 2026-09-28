---
about: flow.FromStage binds exactly one exported top-level field of a stage's Out, never a nested path, so a stage needing several values from another stage's output takes the whole struct field (as reviewstages.ComposeStatus takes reviewdecision.Result)
saw:
  - source/cli/internal/flow/flow.go
  - source/cli/internal/stages/review/compose_status.go
  - source/cli/internal/flows/review/review.go
---

`flow.FromStage(stage, field)` (`internal/flow/flow.go`) names one exported field of the named
stage's `Out`; there is no dotted path. So `ComposeStatusIn` (`internal/stages/review/compose_status.go`)
takes `Result reviewdecision.Result` whole, bound from `decide`'s `Out.Result` in
`internal/flows/review/review.go`, and reads `Result.Verdict` and `Result.Decision` itself — rather
than `Decide` exposing each as its own `Out` field. When designing a stage's `Out`, expose as
top-level fields exactly the values a later stage will bind.
