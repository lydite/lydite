---
name: a-component-nothing-scans-renders-three-unmeasured-rows
kind: invariant
description: A raw-command component or a language with no scanner renders three unmeasured rows in scan, while the toolchain and findings-count paths decide the same question by Enabled rather than Scanned.
anchors:
  - path: source/cli/internal/stages/scan/plan.go
    blob: 6e5358165f49
  - path: source/cli/cmd/lydite/scan.go
    blob: f714a442e1bf
  - path: source/cli/internal/stages/scan/toolchains.go
    blob: dd3ead1f862f
  - path: source/cli/internal/stages/record/findings.go
    blob: bbc727310d74
  - path: source/cli/internal/scanlang/scanlang.go
    blob: e4224f1d7728
confidence: verified
---

`PlanComponents` (`internal/stages/scan/plan.go`, `entry.Disposition = Unscanned` near line 110) marks a component `Unscanned` when `scanlang.Scanned(c.ScanLang())` is false — which includes a raw command stating no language. `recordComponents` (`cmd/lydite/scan.go:163`) renders every `Unscanned` entry through `unscannedRows`: `scan(<name>)`, `licence(<name>)` and `findings(<name>)`, all `ui.StatusUnmeasured`, each with its own reason (`unscannedReason` separates "declares a raw command" from "language has no scanner"), so the verdict stays `pass` and nothing is silent.

Two other paths decide "does anything scan this component" differently: `scanUnits` (`internal/stages/scan/toolchains.go`, what gets a toolchain) and `FindingCounts` (`internal/stages/record/findings.go`, what records a zero per gate) both skip through `lang == "" || !scanlang.Enabled(...)`, not `scanlang.Scanned`. That is correct only because `scanlang.Enabled` answers false for every language it has no config key for, and its keys are exactly today's `Scanned` languages. A language given an `Enabled` key without a `Scanned` entry would be provisioned a toolchain and seeded zero counts while `PlanComponents` renders it unscanned. See [[scanlang-gates-scan-dispatch]].
