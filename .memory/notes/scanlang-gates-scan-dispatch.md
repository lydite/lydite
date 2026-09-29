---
name: scanlang-gates-scan-dispatch
kind: gotcha
description: scanlang.Scanned is asked before any per-language scan dispatch, so a language missing from it makes every other case dead code silently, while a missing checksFor or licenceGateFor case is a loud refusal.
anchors:
  - path: source/cli/internal/scanlang/scanlang.go
    blob: e4224f1d7728
  - path: source/cli/internal/stages/scan/plan.go
    blob: 6e5358165f49
  - path: source/cli/internal/stages/scan/checks.go
    blob: 3e020ff657c6
  - path: source/cli/internal/stages/scan/licences.go
    blob: b63a436db7d9
  - path: source/cli/cmd/lydite/scan.go
    blob: f714a442e1bf
  - path: source/cli/internal/stages/record/findings.go
    blob: bbc727310d74
confidence: verified
---

`PlanComponents` (`internal/stages/scan/plan.go`) asks `scanlang.Scanned(lang)` (`scanlang.go:27`) first and marks anything it answers false for `Unscanned`, which `recordComponents` (`cmd/lydite/scan.go:163`) renders as `unscannedRows`. Only a `Scan` entry ever reaches `RunChecks`' `checksFor` (`internal/stages/scan/checks.go`) or `GateLicences`' `licenceGateFor` (`licences.go:126`). So a `case` added only to `checksFor`, `licenceGateFor`, `scanlang.Enabled` (`scanlang.go:38`) or `scan.go`'s `scannerGates` (`:386`) is dead code until the language is also in `scanlang.Scanned` — a build that passes and a scanner that never runs.

The reverse omission is loud: a language in `Scanned` (and switched on by `Enabled`) with no `checksFor` case makes `RunChecks` error ("is planned for scanning and has no language checks"), and one with no `licenceGateFor` case makes `GateLicences` error ("has no licence gate") before anything is read or checked out — both stop the flow under the default FailFlow policy. `licenceGateFor` names Go, Rust, TypeScript and Shell (`noLicenceGate`, `licences.go:135`, for a scanned language with no dependency set, rendered as an unmeasured `licence(<name>)` row) and its `default` returns an error, per `agentic/rules/refuse-an-unhandled-grammar-rather-than-fall-through.md`. `scannerGates`, handed to the record flow and called by `FindingCounts` (`internal/stages/record/findings.go`), has no such refusal: it returns nil for an unnamed language, so a missing case silently records no per-gate zero.

A new scanned language therefore touches `scanlang.Scanned` and `Enabled`, `checksFor`, `licenceGateFor` and `scannerGates` — plus `offByDefaultRows` if it ships off ([[off-by-default-language-needs-its-own-unmeasured-rows]]).
