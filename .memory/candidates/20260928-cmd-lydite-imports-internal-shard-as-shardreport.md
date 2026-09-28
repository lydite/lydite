---
about: cmd/lydite's plan.go declares a package-level type named shard, so every cmd/lydite file importing internal/shard must alias it (shardreport); internal/shard also keeps its own copy of the report document's path, held to cmd/lydite's by one test
saw:
  - source/cli/cmd/lydite/plan.go
  - source/cli/cmd/lydite/fold.go
  - source/cli/cmd/lydite/fold_shards_test.go
  - source/cli/cmd/lydite/reports.go
  - source/cli/internal/shard/shard.go
  - source/cli/internal/stages/shards/shards.go
---

`cmd/lydite/plan.go` (line ~135) declares `type shard struct { Name string; Components
[]string; ... }` — one CI job's worth of components, what `lydite test plan` emits. Because
`package main` already owns the identifier `shard`, `internal/shard` cannot be imported under
its own name anywhere in `cmd/lydite`: `fold.go` (line ~13) and `fold_shards_test.go` (line ~9)
both import it as `shardreport "lydite/lydite/internal/shard"`. `fold.go`'s `shardInputs` also
names its loop variable `shard` (`for _, shard := range shards`), shadowing the plan type inside
that loop. A new `cmd/lydite` file that needs `shard.Shard`/`shard.Read` has to use the same
alias; renaming the plan type would free the name but touches `plan.go`'s `shardsOf`,
`uniqueNames` and the plan tests.

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
