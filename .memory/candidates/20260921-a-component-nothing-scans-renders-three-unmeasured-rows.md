---
about: a raw-command component (Lang "") or a language with no scanner renders three unmeasured rows in scan, so its absence never reads as a pass
saw:
  - source/cli/cmd/lydite/scan.go
  - source/cli/cmd/lydite/record.go
---

`unscannedRows` emits `scan(<name>)`, `licence(<name>)` and `findings(<name>)`, all
`ui.StatusUnmeasured`, each with its own reason, so the verdict stays `pass` and nothing is silent.
`findings(<name>)` has no gate constant behind it; publish treats it as an ordinary row by label.
`record.go`'s `findingCounts` and `scan.go`'s `scanUnits` still skip such a component through
`lang == "" || !langEnabled(...)`, which is correct today but shares the default-false trap
`scannedLang` closes in the scan loop.
