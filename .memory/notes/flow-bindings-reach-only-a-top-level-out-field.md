---
name: flow-bindings-reach-only-a-top-level-out-field
kind: gotcha
description: flow.FromStage binds exactly one top-level exported field of a stage's Out, with no nested path or promoted field, so a flow needing one sub-field gets a flat restatement on the producing stage's Out.
anchors:
  - path: source/cli/internal/flow/flow.go
    blob: 9b479e3a69ed
  - path: source/cli/internal/flow/run.go
    blob: 0e4ecd236760
  - path: source/cli/internal/stages/scan/sources.go
    blob: 2535acbff48e
  - path: source/cli/internal/flows/scan/scan.go
    blob: 72c7518c93b8
confidence: verified
---

`flow.FromStage(stage, field)` (`internal/flow/flow.go`) records the field name as one string. `Flow.resolve` looks it up with `exportedField` (`flow.go:352`), which compares `sf.Name == name` over the Out type's own fields — so `"Config.Semgrep.Enabled"` matches nothing, and a field promoted from an embedded struct is deliberately not reached. A miss is a `Build` error. The resolved source keeps only `sf.Index[0]`, and at run time `Flow.value` (`run.go`) reads `r.outputs[src.stage].Field(src.field)` — one level. `When`/`Unless` go through the same `resolve` with a `bool` target, so a condition must be a top-level `bool` field.

Consequence in the scan flow: `LoadConfigOut` (`internal/stages/scan/sources.go`) carries the whole `Config` and restates `SemgrepEnabled`, `SecretsEnabled` and `SemgrepConfig` as top-level fields, and `internal/flows/scan/scan.go` binds `When(flow.FromStage(StageLoadConfig, "SemgrepEnabled"))` and so on. Likewise `reviewstages.ComposeStatus` takes `reviewdecision.Result` whole (bound from `decide`'s `Out.Result`) rather than each field separately. When designing a stage's `Out`, expose as top-level fields exactly the values a later stage will bind; there is no projection form.
