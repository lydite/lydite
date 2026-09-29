---
about: an acknowledged mutant is answered by its declaration even when the state holds a verdict for it, and is never passed to Record
saw:
  - source/cli/internal/mutation/executor.go
  - source/cli/internal/mutation/state.go
  - source/cli/internal/stages/mutation/run.go
---

`MutantID` leaves `Reason` out, so a recorded survivor for the same site must not outrank a declaration added since. `Execute` therefore checks `Acknowledged` before `Options.Known`, and `Record` skips acknowledged and cut-short results. `reusedCount` in `run.go` mirrors that: acknowledged mutants never count as reused. A declaration edit changes the file, so the tree digest changes too.
