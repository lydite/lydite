---
about: runner.Lang includes languages lydite recognises as source but may run nothing for (Shell) or scan nothing for (Python); runner.Runs answers "is there a runner", internal/scanlang.Scanned answers "is there a scanner", and the two are independent
saw:
  - source/cli/internal/runner/runner.go
  - source/cli/internal/orphan/orphan.go
  - source/cli/internal/stages/scan/plan.go
  - source/cli/internal/scanlang/scanlang.go
  - source/cli/internal/crap/treesitter.go
  - source/cli/internal/shell/shell.go
---

`runner.SourceExts()` and `LangForExt` answer "is this file source", and cover `Python` and
`Shell` (`.py`, `.sh`, `.bash`); Python has a runner (`python-pytest`) and no scanner, Shell has
a scanner (`internal/shell`, ShellCheck) and no runner — see #187, settled that Shell never gets
one, since `runner.Runs` flipping true for it would make `crap.skipped`
(`internal/crap/treesitter.go`, `ok && runner.Runs(lang)`) report every `.sh` as skipped forever
with no shell grammar to check against. `runner.Runs(lang)` is derived from the registry and is the
only answer to "is there a runner" — code that reaches a `Lang` through a runner never meets a
runner-less one; `crap.skipped` and similar test-side path-driven code must still ask `Runs`
before treating a file as something a test-side gate could act on.

`internal/scanlang.Scanned(lang)` (ADR 0056) is a separate, deliberately runner-independent
question: "does lydite have a scanner for this language at all" — an explicit enumeration (Go,
Rust, TypeScript, Shell), not derived from `runner.Runs`. It is the one list `PlanComponents`
(`internal/stages/scan/plan.go`) and `orphan.Unscanned` (`internal/orphan/orphan.go`) both read,
so the scan and the unscanned warning cannot disagree about which languages have scanners.
(`cmd/lydite/scan.go` still carries a `scannedLang` wrapper over it, read only by `scan_test.go`.)
`PlanComponents` asks `scanlang.Scanned` before `scanlang.Enabled`: `Enabled` answers false for any
language it has no config key for, so a language with a runner (or a declared `lang:`) and no
scanner would otherwise land in the `Disabled` disposition — as though the repository had switched
it off — instead of `Unscanned` with its three unmeasured rows. Having a runner and having a scanner
are independent in both directions — Python has a runner and no scanner, and `lang: shell` (no
runner at all) has a scanner, gated off by default behind `shell.enabled` in `.lydite/config.yml`
since ShellCheck fails on every diagnostic including style.
