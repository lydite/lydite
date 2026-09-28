---
about: internal/scanlang.Scanned is asked before any scan dispatch runs, so a new scanned language needs its scanlang entry plus a case in each of the scan's per-language switches; missing scanlang is silent, missing a switch case is a loud refusal
saw:
  - source/cli/internal/scanlang/scanlang.go
  - source/cli/internal/stages/scan/plan.go
  - source/cli/internal/stages/scan/checks.go
  - source/cli/internal/stages/scan/licences.go
  - source/cli/cmd/lydite/scan.go
  - source/cli/cmd/lydite/record.go
  - source/cli/internal/stages/record/findings.go
---

`PlanComponents` (`internal/stages/scan/plan.go`) asks `scanlang.Scanned(lang)` first and marks
anything it answers false for `Unscanned`, which `recordComponents` (`cmd/lydite/scan.go`) renders
as `unscannedRows`. Only a `Scan` entry ever reaches `RunChecks`' `checksFor`
(`internal/stages/scan/checks.go`) or `GateLicences`' `licenceGateFor`
(`internal/stages/scan/licences.go`). So a `case` added only to `checksFor`, `licenceGateFor`,
`scanlang.Enabled`, or `scan.go`'s `scannerGates` is dead code until the language is also added to
`scanlang.Scanned` — a build that passes and a scanner that never runs.

The reverse omission is loud: a language in `scanlang.Scanned` (and switched on by
`scanlang.Enabled`) with no `checksFor` case makes `RunChecks` return an error ("is planned for
scanning and has no language checks") before any check runs, and one with no `licenceGateFor` case
makes `GateLicences` return "has no licence gate" before anything is read or checked out — both
stop the flow under the default FailFlow policy. `scannerGates` (`cmd/lydite/scan.go`, handed by
`cmd/lydite/record.go` into the record flow as its `ScannerGates` input and called by
`recordstages.FindingCounts` in `internal/stages/record/findings.go`) has no such refusal: it returns nil for an unnamed language, so a
missing case there silently records no per-gate zero. A new scanned language therefore touches
`scanlang.Scanned` and `scanlang.Enabled`, `checksFor`, `licenceGateFor`, and `scannerGates` — plus
`offByDefaultRows` in `scan.go` if it ships off by default.
