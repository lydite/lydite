---
about: apidiff flags an added interface method as incompatible but an added struct field as compatible, and a widened struct field type as incompatible — a gate reading it must check Change.Compatible, never the presence of a change
saw:
  - source/cli/internal/apisurface/testdata/interface-method.report.txt
  - source/cli/internal/apisurface/testdata/compatible-addition.report.txt
  - source/cli/internal/apisurface/testdata/widened-field.report.txt
  - docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md
---

Measured against `golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba` via
`internal/apisurface`'s probe (`probe_test.go`, `TestApidiffReportsEachProbeShape`), five shapes
against a base module:

- a removed exported function → incompatible
- a changed exported function signature → incompatible
- a method added to an exported interface → **incompatible** (`Store.Put: added`) — this breaks
  every implementer outside the module while breaking nobody who only calls it, and `apidiff`
  correctly calls it a break rather than ordinary growth
- an exported struct field's type widened `int` → `int64` → **incompatible**
- an added exported function, and an added struct field → **both compatible**

So "was anything reported" is not the gate: an added struct field and a widened one both produce
a `Change`, but only one is a break. `internal/apisurface.Compare` filters on `!c.Compatible`
before ever constructing a finding — reading presence alone would fire the gate on ordinary
growth (the added-function/added-field case) as often as on a real break.
