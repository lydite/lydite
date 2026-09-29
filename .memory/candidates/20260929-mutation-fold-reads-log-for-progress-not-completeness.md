---
about: the mutation shard fold's completeness check still reads only <dir>/mutation.json, but the fold now also reads a shard's mutation.log — for a cancelled shard's failing detail, never for completeness
saw:
  - source/cli/internal/shard/shard.go
  - source/cli/cmd/lydite/fold.go
  - source/cli/internal/stages/mutation/merge.go
  - source/cli/cmd/lydite/mutation_merge.go
---

Supersedes a prior candidate on the same subject, staged before this branch's fix.

`shard.Read(dir, "mutation")` is unchanged: it opens exactly `<dir>/mutation.json`, and
`shardInputs` (`cmd/lydite/fold.go`) still adds the `StatusFail` "no mutation report" row on a
shard where that read fails — the completeness check (`shardsRow`) still cares only about
`mutation.json` existing and parsing, never about its content or about `mutation.log`. That part
of the prior candidate's claim is unchanged and still true.

What changed: a shard whose `mutation.json` is missing (a job cancelled mid-run) is no longer
treated as a bare, causeless absence. `internal/stages/mutation/merge.go` now reads that shard's
`mutation.log` through a dedicated hook offered only for an unread shard — `test merge` never
receives it, and a test pins its output byte-for-byte unchanged. The log is parsed for two
things a per-mutant `start <mutant>` line (added by `internal/mutation/executor.go`) makes
possible: which mutant(s) started and never logged a finish line, and, from the cost-projection
line `costProjectionIn` already parsed, how many mutants the run intended against how many
finished. When the log carries neither a projection line nor any start line, the detail says the
run stopped before any mutant was generated — the shape an install or baseline hang leaves.

The failing row's status and the completeness verdict are both unchanged: a cancelled shard
still fails, and a declared component with no `mutation.json` still fails `shardsRow`. Only the
row's detail text changed, from a generic "no mutation report" to naming what was in flight.
