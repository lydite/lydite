---
name: record-stages-read-other-commands-documents-through-a-stateful-reportreader
kind: invariant
description: Another command's report document reaches record's stages only through recordstages.ReportReader and stage-owned boundary types, and the CLI's recordReports adapter is stateful because Fold methods fold what the reads returned rather than reading again.
anchors:
  - path: source/cli/internal/stages/record/record.go
    blob: 302abc7fc5c0
  - path: source/cli/internal/stages/record/reports.go
    blob: e0eb7dc136d4
  - path: source/cli/cmd/lydite/record.go
    blob: 8bd9f8f28805
confidence: suspect
---

`measurementsDoc` (`cmd/lydite/measurements.go`, owned by `lydite test`), `mutantsDoc` (owned by `lydite mutation`) and the scan report are declared in `cmd/lydite`, which is `package main` — nothing can import it, so no stage can be typed on those documents. ADR 0069 makes the seam explicit: `recordstages.ReportReader` (`internal/stages/record/record.go:110`) has five methods — `ReadMeasurements`, `ReadScan`, `ReadMutants`, `FoldMeasurements`, `FoldMutants` — typed in the stage package's own boundary types (`Measurements`, `Mutants`, `Scan`, holding only the fields a stage reads). `cmd/lydite/record.go`'s `recordReports` implements it by calling the owning code and converting. Read errors pass through unchanged so `errors.Is(err, os.ErrNotExist)` still separates an absent document from an unparseable one in `readRow`. The ADR's pattern for any command folding another's document into its flow: stage-owned interface plus boundary types, never a `package main` import.

**The adapter is stateful, and has to be.** The contract says `FoldMeasurements`/`FoldMutants` are given only directories `ReadMeasurements` answered without error and "fold what those reads returned rather than reading again". `ReadReports` (`reports.go`) collects the successful reads and hands them back to the fold methods, so `recordReports` keeps `measurements map[string]measurementsDoc` and `mutants map[string]mutantsDoc` keyed by directory (`cmd/lydite/record.go:455-456`), and its Fold methods look each directory up there, erroring on a miss instead of re-reading. An adapter that re-read in Fold could fold a different document from the one reported as a row, and one `recordReports` must not be reused across runs — `newRecordReports()` (`:459`, used at `:113`) builds a fresh one per run. `ReadScan` keeps nothing, since scans are never folded by the reader. The interface and struct locations were re-checked; the rest is the explorer's reading, hence `suspect`.
