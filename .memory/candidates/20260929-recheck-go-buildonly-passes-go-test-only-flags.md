---
about: BuildOnly's go build no longer receives go-test-only flags; goBuildArgs drops them through the goBuildRejected table
saw:
  - source/cli/internal/runner/runner.go
  - source/cli/internal/runner/runner_test.go
targets: go-buildonly-passes-go-test-only-flags-to-go-build
verdict: now-false
---

`buildGoTest`'s `BuildOnly` case (`internal/runner/runner.go`) returns `go build` +
`goBuildArgs(pkgs)`, which runs `dropFlags(args, true, goBuildRejected)`. `goBuildRejected` holds
the coverage flags plus every `go help testflag` flag `go build` does not define: `-timeout`,
`-run`, `-skip`, `-count`, `-failfast`, `-short`, `-parallel`, `-cpu`, `-shuffle`, `-list`, the
benchmark, fuzz, profiling and trace flags, `-c`, `-exec`, `-vet`, `-fullpath`, `-artifacts` and
`-gocoverdir`. `TestBuildOnlyDropsTheFlagsGoBuildRejects` and
`TestBuildOnlyDropFilterKeepsWhatGoBuildAccepts` (`runner_test.go`) pin it. Plain and
Instrumented still drop only the coverage flags, so a declared `-timeout` reaches them.

What still holds: a go-test-only flag missing from `goBuildRejected` (one a new Go release adds)
reaches `go build`, fails with `flag provided but not defined`, and every mutant is classed as not
compiling. `GOFLAGS=-timeout=…` still leaks into `internal/mutation`'s nested `go test` and
breaks `TestASuiteThatHangsTimesOutAndCountsAsKilled`.
