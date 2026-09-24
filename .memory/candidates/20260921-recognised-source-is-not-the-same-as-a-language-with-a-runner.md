---
about: runner.Lang includes languages lydite recognises as source but may run nothing for (Shell today) or scan nothing for; runner.Runs answers "is there a runner", internal/scanlang.Scanned answers "is there a scanner", and the two are independent
saw:
  - source/cli/internal/runner/runner.go
  - source/cli/internal/orphan/orphan.go
  - source/cli/cmd/lydite/scan.go
  - source/cli/internal/scanlang/scanlang.go
  - source/cli/internal/crap/treesitter.go
---

`runner.SourceExts()` and `LangForExt` answer "is this file source", and cover `Python` and
`Shell` (`.py`, `.sh`, `.bash`); Python has a runner (`python-pytest`), Shell has none. `runner.Runs(lang)` is derived from
the registry and is the only answer to "is there a runner" — code that reaches a `Lang` through
a runner never meets a runner-less one; `crap.skipped` and similar test-side path-driven code
must still ask `Runs` before treating a file as something a test-side gate could act on.

`internal/scanlang.Scanned(lang)` (ADR 0056, lydite/lydite#252) is a separate, deliberately
runner-independent question: "does lydite have a scanner for this language at all". It is the
one enumeration `scan.go`'s `scannedLang` (now a thin wrapper) and `orphan.go`'s `Unscanned` both
read, so the two cannot disagree about which languages have scanners. `scannedLang` is asked
before `langEnabled` in the scan loop: `langEnabled` answers false for any language it has no
config key for, so a language with a runner (or a declared `lang:`) and no scanner would
otherwise be skipped as though the repository had switched it off, with no row. Having a runner
and having a scanner are independent in both directions — Python has a runner and no scanner, and
a `lang: shell`-only component (no runner at all) can still have a scanner once one lands.
