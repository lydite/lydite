---
about: BuildOnly's `go build` receives none of a component's go-test-only flags; a flag missing from `goBuildRejected` makes every mutant unviable, and GOFLAGS is not a way to bound the baseline
saw:
  - source/cli/internal/runner/runner.go
  - .lydite/components.yml
---

`buildGoTest`'s `BuildOnly` case (`internal/runner/runner.go`) returns `go build` +
`goBuildArgs(pkgs)`, which runs `dropFlags` with the `goBuildRejected` table: the coverage flags
plus every `go help testflag` flag `go build` does not define (`-timeout`, `-run`, `-count`,
`-parallel`, profiling, benchmark and fuzz flags, `-c`, `-exec`, `-vet`, `-fullpath`,
`-artifacts`, `-gocoverdir`). Plain and Instrumented only drop coverage flags, so a declared
`-timeout` still bounds the baseline and per-mutant runs.

A go-test-only flag missing from `goBuildRejected` reaches `go build`, which fails with
`flag provided but not defined`. Mutation compiles each mutant through `BuildOnly` first, so every
mutant is then classed as not compiling, and the run reports
`mutation(<component>) … not measured — N did not compile` and exits 0. A new Go release adding
a test flag needs adding there. Value-taking flags must also be in `goTestValueFlags`, or their
separately passed value is left behind as a package pattern.

This repository's `cli` component declares `-timeout 30m` in `.lydite/components.yml` because
`cmd/lydite`'s suite under `-race` outruns `go test`'s 10-minute default. Setting
`GOFLAGS=-timeout=…` instead is not equivalent: it leaks into `internal/mutation`'s own nested
`go test` and breaks `TestASuiteThatHangsTimesOutAndCountsAsKilled`.
