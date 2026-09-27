---
about: a Go component declaring a go-test-only flag such as -timeout in its args makes every mutant unviable, because the BuildOnly variant hands those args to `go build`, which rejects them; mutation then reports "not measured" and exits 0
saw:
  - source/cli/internal/runner/runner.go
  - .lydite/components.yml
---

`buildGoTest`'s `BuildOnly` case (`internal/runner/runner.go`) returns
`go build` + `goTestUninstrumented(pkgs)`. `goTestUninstrumented` strips coverage flags only
(`dropCoverage`, see `agentic/rules/a-coverage-flag-reaches-only-the-instrumented-variant.md`),
so any other `go test`-only flag a component declares in `.lydite/components.yml`'s `args:` —
`-timeout`, `-run`, `-count`, `-parallel` — reaches `go build`, which fails with
`flag provided but not defined`. Mutation compiles each mutant through `BuildOnly` first, so every
mutant is classed as not compiling and the run reports
`mutation(<component>) … not measured — N mutant(s) … N did not compile`, exiting 0.

This matters for this repository's own `cli` component: `cmd/lydite`'s suite outruns `go test`'s
built-in 10-minute default under `-race`, so mutation's baseline dies on the timeout panic unless
a `-timeout` is declared — and declaring one hits the failure above. Setting
`GOFLAGS=-timeout=…` for the invocation is not a workaround: it leaks into
`internal/mutation`'s own nested `go test` and breaks
`TestASuiteThatHangsTimesOutAndCountsAsKilled`. Tracked as lydite/lydite#289.
