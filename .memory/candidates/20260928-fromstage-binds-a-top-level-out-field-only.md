---
about: flow.FromStage names one top-level exported field of a stage's Out, never a path into a nested struct, so a later stage that needs part of a nested value needs that value flattened onto the Out
saw:
  - source/cli/internal/flow/flow.go
  - source/cli/internal/flow/run.go
  - source/cli/internal/stages/queue/event.go
---

`flow.FromStage(stage, field)` resolves `field` with `exportedField(from.out, b.field)`
(`internal/flow/flow.go:308`) and reads it at run time as `r.outputs[src.stage].Field(src.field)`
(`internal/flow/run.go:299`) — a single field lookup on the producing stage's `Out`, with no
dotted-path support. A stage whose natural output is a struct (the queue entry `forge` parses out
of a `merge_group` payload) cannot hand one of that struct's fields to a later stage's `In`.

The queue flow's `load-event` stage therefore returns `PullRequest int` flat on `LoadEventOut`
rather than the parsed entry (`internal/stages/queue/event.go`), so `submit-comparison` can bind
`PullRequest` by name. Designing a stage's `Out`, flatten every value a later stage binds; a
nested struct is only readable by the CLI through `flow.Output[T]`.
