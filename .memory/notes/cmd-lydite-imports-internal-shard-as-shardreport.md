---
name: cmd-lydite-imports-internal-shard-as-shardreport
kind: gotcha
description: cmd/lydite declares a package-level type shard in plan.go, so every file importing internal/shard aliases it shardreport, and internal/shard keeps its own copy of the report path held to cmd/lydite's by one test.
anchors:
  - path: source/cli/cmd/lydite/plan.go
    blob: 8ab5defe6fed
  - path: source/cli/cmd/lydite/fold.go
    blob: 454c64d57793
  - path: source/cli/internal/shard/shard.go
    blob: 66b79cc43ab0
  - path: source/cli/cmd/lydite/fold_shards_test.go
    blob: f62751760823
confidence: verified
---

`cmd/lydite/plan.go:135` declares `type shard struct { ... }` — one CI job's worth of components, what `lydite test plan` emits. Because `package main` already owns the identifier, `internal/shard` cannot be imported under its own name anywhere in `cmd/lydite`: `fold.go:13` and `fold_shards_test.go` import it as `shardreport "lydite/lydite/internal/shard"`, and `fold.go`'s `shardInputs` loop variable `shard` shadows the plan type. A new `cmd/lydite` file needing `shard.Shard`/`shard.Read` must use the same alias; renaming the plan type would touch `shardsOf`, `uniqueNames` and the plan tests. The stage package is named `shardstages` (`internal/stages/shards`) so a flow can import both; `internal/stages/mutation/merge.go` imports `internal/shard` unaliased — the collision is `cmd/lydite`'s alone.

`internal/shard.Read` finds a document by name through its own unexported `documentPath(dir, command)` = `<dir>/<command>.json` (`shard.go:47`), because nothing under `internal/` may import `cmd/lydite`, whose `documentPath` (`reports.go`) is what `saveDocument` writes through. `TestShardReadReadsTheDocumentSaveDocumentWrites` (`fold_shards_test.go:18`) saves a report for `test`, `mutation` and `scan` and reads each back through `shardreport.Read`, so the copies cannot drift silently — otherwise every fold would read each shard as missing its report.
