---
about: sharedTrees' ancestor pruning compares each path only to the last one kept, so a byte-adjacent sibling can leave a nested path reported twice
saw:
  - source/cli/internal/scheduler/scheduler.go
---

`sharedTrees` sorts the overlapping paths and drops any covered by the previously kept one. Plain
string sort does not always put a descendant directly after its ancestor: a sibling whose name
extends the ancestor with a byte below `/` (`-` or `.`) sorts between them, so
`packages/tokens` < `packages/tokens-x` < `packages/tokens/dist` leaves `packages/tokens/dist`
unpruned. Only the report is affected — it names the same contention twice; the locking, `Pairs`
and shard grouping are computed from overlap, not from the pruned list. A fix compares each
candidate against every kept path.
