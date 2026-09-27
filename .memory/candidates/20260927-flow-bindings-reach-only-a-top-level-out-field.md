---
about: flow.FromStage (for With, When and Unless) binds exactly one top-level exported field of a stage's Out — no nested path, no promoted field — so a flow that needs one field of a struct gets a flat restatement on the producing stage's Out
saw:
  - source/cli/internal/flow/flow.go
  - source/cli/internal/flow/run.go
  - source/cli/internal/stages/scan/sources.go
  - source/cli/internal/flows/scan/scan.go
---

`flow.FromStage(stage, field)` (`internal/flow/flow.go`) records the field name as one string.
`Flow.resolve` looks it up with `exportedField(from.out, b.field)`, which walks the Out type's own
fields comparing `sf.Name == name` — so `"Config.Semgrep.Enabled"` matches nothing, and a field
promoted from an embedded struct is deliberately not reached either (the helper's doc says so).
A miss is a `Build` error ("stage %q's %s has no exported field %q"). The resolved source keeps
only `sf.Index[0]`, and at run time `Flow.value` (`internal/flow/run.go`) reads
`r.outputs[src.stage].Field(src.field)` — one level, no traversal. `When`/`Unless` go through the
same `resolve`, with `boolType` as the target, so a condition must be a top-level `bool` field.

Consequence, seen in the scan flow: `LoadConfigOut` (`internal/stages/scan/sources.go`) carries the
whole `Config` and also restates `SemgrepEnabled`, `SecretsEnabled` (bools) and `SemgrepConfig`
(string) as top-level fields, with a doc comment saying why. `internal/flows/scan/scan.go` binds
`When(flow.FromStage(StageLoadConfig, "SemgrepEnabled"))`, `When(... "SecretsEnabled")` and
`With("Config", flow.FromStage(StageLoadConfig, "SemgrepConfig"))`. The alternatives are passing
the whole struct into the reading stage's In (which the scan flow does for `Config` elsewhere) or
adding a restated field to the producer's Out; there is no binding form that projects a sub-field.
