---
about: cmd/lydite still aliases internal/shard as shardreport in fold.go/fold_shards_test.go, but the package-main `shard` type that originally forced the alias is gone — plan.go's shard type was deleted when `test plan` moved onto Flow (teststages.PlanShard replaced it); internal/shard also keeps its own copy of the report document's path, held to cmd/lydite's by one test
saw:
  - source/cli/cmd/lydite/fold.go
  - source/cli/cmd/lydite/fold_shards_test.go
  - source/cli/cmd/lydite/reports.go
  - source/cli/internal/shard/shard.go
  - source/cli/internal/stages/shards/shards.go
  - source/cli/internal/stages/test/plan.go
---

This candidate originally described `cmd/lydite/plan.go` declaring `type shard struct { Name
string; Components []string; ... }` (one CI job's worth of components) and claimed `package
main` owning the identifier `shard` was why `internal/shard` had to be imported under an alias
everywhere in `cmd/lydite`. The `test plan`/`test merge`-onto-Flow branch deleted that type
outright: `lydite test plan`'s shard grouping now returns `teststages.PlanShard`
(`internal/stages/test/plan.go`), and nothing in `cmd/lydite` declares a type or package-level
identifier named `shard` any more — confirmed by grep, 2026-09-28.

Despite that, `fold.go` (line ~11) and `fold_shards_test.go` (line ~9) still import
`internal/shard` as `shardreport "lydite/lydite/internal/shard"` rather than unaliased. The only
remaining local reason is `fold.go`'s `shardInputs`, whose loop uses `shard` as its range
variable (`for _, shard := range shards`) — a real name, not a type, so aliasing the import is
no longer forced by a compile-time collision, only kept (perhaps just not reverted) to avoid
shadowing the package name inside that loop. A new `cmd/lydite` file reaching for
`shard.Shard`/`shard.Read` is free to import it unaliased now; matching the existing
`shardreport` spelling is a style choice, not a requirement.

The stage package is named apart for a related reason: `internal/stages/shards` is package
`shardstages` (its package doc says so, `shards.go` lines ~1-6) so a flow definition can import
both it and `internal/shard` without renaming either. `internal/stages/mutation/merge.go`
imports `internal/shard` unaliased — the collision is only `cmd/lydite`'s.

`internal/shard.Read` finds a document by name alone, through its own unexported
`documentPath(dir, command)` = `<dir>/<command>.json` (`shard.go` line ~47), because nothing
under `internal/` may import `cmd/lydite`, whose own `documentPath` (`reports.go` line ~230) is
what `saveDocument` writes through. `TestShardReadReadsTheDocumentSaveDocumentWrites`
(`fold_shards_test.go` line ~18) saves a report for `test`, `mutation` and `scan` and reads each
back through `shardreport.Read`, so the two copies cannot drift silently — were they to
disagree, every fold would read each shard as missing its report.
