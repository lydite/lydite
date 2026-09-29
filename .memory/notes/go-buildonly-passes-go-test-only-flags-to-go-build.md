---
name: go-buildonly-passes-go-test-only-flags-to-go-build
kind: gotcha
description: A Go component declaring a go-test-only flag such as -timeout makes every mutant unviable, because the BuildOnly variant hands those args to go build, which rejects them, and mutation reports not measured with exit 0.
anchors:
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
  - path: .lydite/components.yml
    blob: de5da43c2cdf
confidence: verified
---

`buildGoTest`'s `BuildOnly` case (`internal/runner/runner.go:400`) returns `go build` + `goTestUninstrumented(pkgs)`. `goTestUninstrumented` (`:513`) strips coverage flags only (`dropCoverage`, see `agentic/rules/a-coverage-flag-reaches-only-the-instrumented-variant.md`), so any other `go test`-only flag a component declares in `.lydite/components.yml`'s `args:` — `-timeout`, `-run`, `-count`, `-parallel` — reaches `go build`, which fails with `flag provided but not defined`. Mutation compiles each mutant through `BuildOnly` first, so every mutant is classed as not compiling and the run reports `mutation(<component>) … not measured — N mutant(s) … N did not compile`, exiting 0.

It bites this repository's own `cli` component (`args: ["-race", "./..."]`): the suite can outrun `go test`'s 10-minute default, and declaring `-timeout` to fix that hits this. `GOFLAGS=-timeout=…` is a workaround only for the baseline: it leaks into `internal/mutation`'s own nested `go test` and breaks `TestASuiteThatHangsTimesOutAndCountsAsKilled`. Tracked as lydite/lydite#289. See [[cli-suite-outruns-gos-default-test-timeout]].
