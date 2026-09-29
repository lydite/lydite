---
about: an incomplete mutation component is written to mutants.json under `incomplete_components`, never under `components`, so a reader unaware of the marker cannot take a partial count for a complete score
saw:
  - source/cli/internal/mutation/counts.go
  - source/cli/internal/stages/mutation/record.go
  - source/cli/cmd/lydite/mutation_merge.go
---

`CountsDocument.IncompleteComponents` (`internal/mutation/counts.go`) holds the components a deadline stopped, each carrying `ComponentCounts.Incomplete{Measured, Wanted}`. `ReadCounts` refuses a marked entry under `components`, an unmarked one under `incomplete_components`, and a component under both. Every reader ignores keys it does not know, so a marker alone on a `components` entry would read to an older `mutation merge` or `test record` as a finished score; under its own key the same reader finds the component absent, its answer for a component nothing measured to completion. `recordedMutants` only iterates `Components`, which is why `lydite test record` never receives a partial count.

`FoldCounts` keeps the first incomplete entry whole and does not sum Measured/Wanted: a component belongs to one shard, so a second entry is the same mutants measured twice. `mutation_merge.go`'s `killedOf` fallback (a score read back out of a rendered row) skips a row carrying the "N of M measured, rerun to resume" line, so an incomplete survivor row is never read as a finished score.
