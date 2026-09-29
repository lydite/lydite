---
about: the cli component declares -timeout 30m, so its baseline is no longer bound by go test's 10-minute default, and declaring -timeout is the fix rather than a trap
saw:
  - .lydite/components.yml
  - source/cli/internal/runner/runner.go
targets: cli-suite-outruns-gos-default-test-timeout
verdict: now-false
---

`.lydite/components.yml` declares `cli` with `args: ["-race", "-timeout", "30m", "./..."]`. The
note's "Do not add `-timeout` to `args:`" and its `GOFLAGS=-timeout=30m` workaround are out of
date: `goBuildArgs` (`internal/runner/runner.go`) keeps `-timeout` out of BuildOnly's `go build`,
while Plain and the Instrumented baseline receive it. The rest of the note (the suite's measured
duration, the baseline becoming `KindBaselineFailed` when it dies on a timeout, `--timeout` bounding
only mutants, contended runs over-reporting kills) was not re-checked by this change.
