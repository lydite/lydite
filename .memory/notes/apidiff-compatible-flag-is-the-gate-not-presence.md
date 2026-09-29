---
name: apidiff-compatible-flag-is-the-gate-not-presence
kind: gotcha
description: apidiff reports an added interface method and a widened struct field as incompatible but an added function or struct field as compatible, so a gate must read Change.Compatible, never whether a change exists.
anchors:
  - path: source/cli/internal/apisurface/apisurface.go
    blob: 9ceb618d44c8
  - path: source/cli/internal/apisurface/probe_test.go
    blob: 4d80c1bbe9c1
confidence: verified
---

Measured against `golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba` via the probe in `internal/apisurface/probe_test.go` (`TestApidiffReportsEachProbeShape`), five shapes against a base module:

- a removed exported function → incompatible
- a changed exported function signature → incompatible
- a method added to an exported interface → **incompatible** (`Store.Put: added`) — it breaks every implementer outside the module
- an exported struct field's type widened `int` → `int64` → **incompatible**
- an added exported function, and an added struct field → **both compatible**

So "was anything reported" is not the gate: an added struct field and a widened one both produce a `Change`, but only one is a break. `apisurface.Compare` filters on `!c.Compatible` (`apisurface.go:106`) before constructing any finding; reading presence alone would fire on ordinary growth as often as on a real break. See ADR 0040.
