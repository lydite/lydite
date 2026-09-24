---
about: source/cli/internal/scanlang/scanlang.go
saw: feature/shell-is-scanned branch, adding the shell scanner
---

`internal/scanlang.Scanned` is checked before `cmd/lydite/scan.go`'s per-language dispatch
switch ever runs — `scannedLang` routes anything `scanlang.Scanned` answers false for straight
into `unscannedRows`, so a `case runner.Shell:` added only to the dispatch switch, `langEnabled`,
or `scannerGates` in `scan.go` is dead code until the language is also added to
`scanlang.Scanned`. Adding a new scanned language requires touching both files; missing the
`scanlang` half looks like a build that passes and a scanner that mysteriously never runs.
