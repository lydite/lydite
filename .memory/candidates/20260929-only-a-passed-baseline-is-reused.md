---
about: a recorded baseline is reused only when it passed, and is saved only after coverage.Measure has filled its executed lines
saw:
  - source/cli/internal/stages/mutation/run.go
  - source/cli/internal/mutation/state.go
---

`componentState.baseline` returns `ok && b.Passed`. A failed or too-large baseline is never saved, because its output cannot be replayed from the state, so the next run measures it again and reaches the same answer. A baseline hit still runs `Prepare`, `StartServices` and setup, because a mutant's suite needs them whether or not the baseline is measured. `Baseline.Executed` must be present on a hit, since `generate` selects mutant sites from it.
