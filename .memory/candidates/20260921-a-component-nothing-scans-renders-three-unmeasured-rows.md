---
about: a raw-command component (ScanLang "") or a language with no scanner renders three unmeasured rows in scan, so its absence never reads as a pass; the toolchain and findings-count paths skip such a component by Enabled, not Scanned
saw:
  - source/cli/internal/stages/scan/plan.go
  - source/cli/cmd/lydite/scan.go
  - source/cli/internal/stages/scan/toolchains.go
  - source/cli/internal/stages/record/findings.go
  - source/cli/cmd/lydite/record.go
  - source/cli/internal/scanlang/scanlang.go
---

`PlanComponents` (`internal/stages/scan/plan.go`) marks a component `Unscanned` when
`scanlang.Scanned(c.ScanLang())` is false — which includes a raw command stating no language.
`recordComponents` (`cmd/lydite/scan.go`) renders every `Unscanned` entry through `unscannedRows`,
which emits `scan(<name>)`, `licence(<name>)` and `findings(<name>)`, all `ui.StatusUnmeasured`,
each with its own reason (`unscannedReason` separates "declares a raw command" from "language has
no scanner"), so the verdict stays `pass` and nothing is silent. `findings(<name>)` has no gate
constant behind it; nothing in `cmd/lydite/publish.go` special-cases that label.

Two other paths decide the same "does anything scan this component" question differently:
`scanUnits` (`internal/stages/scan/toolchains.go`, what gets a toolchain provisioned) and
`FindingCounts` (`internal/stages/record/findings.go`, what records a zero per gate) both skip
through `lang == "" || !scanlang.Enabled(...)` (`FindingCounts` via its `LangEnabled` function
argument, which `cmd/lydite/record.go` fills with `cmd/lydite/scan.go`'s `langEnabled` shim), not
through `scanlang.Scanned`. That is correct only because `scanlang.Enabled` answers
false for every language it has no config key for, and its keys are exactly the `Scanned`
languages today. A language given an `Enabled` key without a `Scanned` entry would be provisioned a
toolchain and seeded zero counts while `PlanComponents` renders it unscanned.
