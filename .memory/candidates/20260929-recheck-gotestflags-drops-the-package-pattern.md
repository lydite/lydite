---
about: the shared flag scan is dropFlags with a drop table, not dropCoverage; the package-pattern claim still holds
saw:
  - source/cli/internal/runner/runner.go
targets: gotestflags-drops-the-package-pattern
verdict: still-true
---

The scan is `dropFlags(args []string, keepPackages bool, drop map[string]bool)` in
`internal/runner/runner.go`, with three wrappers: `goTestFlags` (`keepPackages=false`,
`coverageFlags`), `goTestUninstrumented` (`true`, `coverageFlags`) and `goBuildArgs` (`true`,
`goBuildRejected`). The note's pointers to `dropCoverage(args, keepPackages bool)` have moved to it.
`goTestFlags` still drops every non-flag argument, and the one-pass reasoning is unchanged.
