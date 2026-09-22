---
about: runner.Lang includes languages lydite recognises as source but may run nothing for (Shell today); runner.Runs is what separates them, and the enumerated scannedLang is what keeps a runner-less language out of langEnabled's silent opt-out
saw:
  - source/cli/internal/runner/runner.go
  - source/cli/internal/orphan/orphan.go
  - source/cli/cmd/lydite/scan.go
  - source/cli/internal/crap/treesitter.go
---

`runner.SourceExts()` and `LangForExt` answer "is this file source", and cover `Python` and
`Shell` (`.py`, `.sh`, `.bash`); Python has a runner (`python-pytest`), Shell has none. `runner.Runs(lang)` is derived from
the registry and is the only answer to "is there a runner". Code that reaches a `Lang` through a
runner never meets a runner-less one; code that reaches it from a path (`orphan.sourceOf`,
`orphan.Unscanned`, `crap.skipped`) must ask `Runs` before treating the file as something a gate
could act on.

`scan.go`'s `scannedLang` enumerates Go, Rust and TypeScript instead of using `Runs`, and is asked
before `langEnabled`: `langEnabled` answers false for any language it has no config key for, so a
language with a runner and no scanner would otherwise be skipped as though the repository had
switched it off, with no row.
