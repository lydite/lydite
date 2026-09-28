---
about: executil.Result.HitMemoryLimit() tells a memory-bound kill from an unrelated failure by peak/limit >= 1/3, an empirically chosen fraction rather than an equality
saw:
  - source/cli/internal/executil/executil.go
  - source/cli/internal/stages/mutation/budget.go
---

`RLIMIT_DATA` bounds the mappings a process holds while `ru_maxrss` (`Result.MaxRSS`)
counts only the pages it actually touched, so a process killed at the ceiling does
not necessarily report a peak equal to the limit — a runtime that dies on a refused
`mmap` has reserved more than it ever faulted in. Measured ratios at death for a
plain Go child: 64MiB ceiling -> 0.03, 128MiB -> 0.45, 256MiB -> 0.74, 512MiB -> 0.87,
1GiB -> 0.94. The *same* child built with `-race` dies at a much lower ratio because
the race detector's shadow memory mappings count against `RLIMIT_DATA` without ever
being fully resident: 512MiB -> 0.40, 1GiB -> 0.58, 2GiB -> 0.74. An unrelated
failure under a bound it never approached reports ~0.01.

`HitMemoryLimit()` (`executil.go` line ~149) uses `MaxRSS*3 >= MemoryLimit` (peak >= 1/3 of
the limit) as the discriminator — chosen to sit between an ordinary mutation run's expected
ratio (around 0.25, since `memoryBudget` in `internal/stages/mutation/budget.go` derives the
ceiling as `memoryFactor` = 4x the baseline's own peak) and a race-instrumented death's
0.40-0.58. This is workable only because the same file's `minimumMemory` floor is 2GiB — at
ceilings under roughly 256MiB (not reachable through the derivation given that floor) a plain
Go child can die at a ratio the 1/3 threshold would misclassify as "not the memory bound." A
`--memory` override bypasses the floor entirely (`memoryBudget` returns the override as given),
so an operator passing a small bound is exactly the case the threshold was not derived for.
Anyone lowering the floor, or applying `HitMemoryLimit()` to a much smaller bound elsewhere,
should re-derive the threshold rather than assume 1/3 generalizes.
