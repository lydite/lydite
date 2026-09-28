---
about: another command's report document reaches record's stages only through recordstages.ReportReader and stage-owned boundary types, because the document types live in package main; the CLI's recordReports adapter is stateful — it keeps every document it read keyed by directory, because FoldMeasurements/FoldMutants fold what the reads returned rather than reading again
saw:
  - source/cli/internal/stages/record/record.go
  - source/cli/internal/stages/record/reports.go
  - source/cli/internal/stages/record/mutants.go
  - source/cli/cmd/lydite/record.go
  - source/cli/cmd/lydite/measurements.go
  - docs/adr/0069-a-recordings-history-is-a-deferred-closure-and-its-inputs-cross-a-boundary-type.md
---

`measurementsDoc` (`cmd/lydite/measurements.go`, owned by `lydite test`), `mutantsDoc` (owned by
`lydite mutation`) and the scan report are all declared in `cmd/lydite`, which is `package main`
— no package can import it, so no stage can be typed on those documents at all. ADR 0069 makes
the seam explicit: `recordstages.ReportReader` (`internal/stages/record/record.go`) has five
methods — `ReadMeasurements`, `ReadScan`, `ReadMutants`, `FoldMeasurements`, `FoldMutants` — typed
entirely in the stage package's own boundary types `Measurements`, `Mutants`, `Scan` (holding only
the fields a stage reads). `cmd/lydite/record.go`'s `recordReports` implements it by calling the
owning code (`readMeasurements`, `readDocument(documentPath(dir, "scan"))`, `readMutants`,
`foldMeasurements`, `foldMutants`) and converting (`recordedMeasurements`, which also calls the
document's own unexported `snapshot()`, and `recordedMutants`). Read errors pass through
unchanged, so `errors.Is(err, os.ErrNotExist)` still separates an absent document from an
unparseable one in `readRow`. The ADR states this as the pattern for any future command folding
another command's document into its flow: stage-owned interface plus boundary types, never a
`package main` import.

**The adapter is stateful, and has to be.** The `ReportReader` contract says `FoldMeasurements`
and `FoldMutants` are "only ever given directories ReadMeasurements answered without an error, and
fold what those reads returned rather than reading again". `ReadReports`
(`internal/stages/record/reports.go`) collects `Measured`/`Mutated` from successful reads, and
`FoldMeasurements` (same file) and `BindMutants` (`mutants.go`) hand exactly those lists back to
the reader's fold methods. So `recordReports` keeps `measurements map[string]measurementsDoc` and
`mutants map[string]mutantsDoc` keyed by directory, filled by the Read methods; its Fold methods
look each directory up there and error ("no <document> was read from it to fold") on a miss
instead of re-reading. Consequences: an adapter implementation that re-read the directory in
Fold could fold a document different from the one `ReadReports` reported and rendered as a row;
and one `recordReports` must not be reused across runs — `recordBaseline` builds a fresh one
(`newRecordReports()`) per `Run`. `ReadScan` keeps nothing, because scans are never folded by the
reader: `ReadReports` appends each directory's findings and crashes itself.
