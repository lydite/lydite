---
about: cmd/lydite/fold.go's readShards is exercised only by tests, not by any command's own run path
saw:
  - source/cli/cmd/lydite/fold.go
  - source/cli/cmd/lydite/findings_test.go
---

`readShards(rep, reports, command, alongside)` in `fold.go` reads each shard's report
document off disk, adds a `read(<dir>)` row per shard, and calls `shardInputs`. Both
`lydite mutation merge` and `lydite test merge` used to call it directly; both now read
their shards through `shardstages.ReadShards` inside their own flow
(`mutationflow.NewMerge`, `testflow.NewMerge`) instead, and call `shardInputs` directly
with the flow's `ReadShardsOut.Shards` — bypassing `readShards` itself.

As of this branch (`refactor(cli): rewire test merge onto testflow.NewMerge()`),
`readShards` has no caller left in `cmd/lydite`'s command implementations — only
`findings_test.go` (around lines 270 and 305, both passing a nil `alongside` hook) still
calls it, to exercise `shardInputs`'s finding-collection behavior without going through a
flow. It survives `unused` lint only because of those test call sites. A future session
that sees `readShards` still defined and assumes it backs a live command path would be
wrong; conversely, deleting it without first checking `findings_test.go` would break that
test file's only route to `shardInputs`.
