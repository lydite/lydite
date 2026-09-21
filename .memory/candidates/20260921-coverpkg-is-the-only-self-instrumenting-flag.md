---
about: -coverpkg is the only declared flag, across all three runners, that turns on instrumentation as a side effect
saw:
  - source/cli/internal/runner/runner.go
---

`go test -coverpkg=X` enables coverage instrumentation with no separate `-cover` needed, which
is why a Go component's declared `args:` could silently instrument the Plain and BuildOnly
variants (fixed by `dropCoverage`/`goTestUninstrumented` in `runner.go`, lydite/lydite#212).

No equivalent exists in the other two runners: Vitest's `--coverage.include` and sibling
sub-options are inert without the top-level `--coverage` flag, which lydite supplies only on
the Instrumented variant. Cargo selects instrumentation by which runner the component declared
(`cargo-nextest` vs `cargo-llvm-cov-nextest`), not by any flag in `args:`, so no stray argument
can reach for `cargo-llvm-cov`. A future runner variant builder only needs this strip for Go.
