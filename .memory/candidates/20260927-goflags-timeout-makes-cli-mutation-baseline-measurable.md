---
about: the cli component's own lydite mutation baseline hits go test's 10-minute default because .lydite/components.yml's cli args set no -timeout; GOFLAGS=-timeout=30m in the calling environment reaches the baseline's go test and makes it measurable without editing the declaration
saw:
  - .lydite/components.yml
  - source/cli/cmd/lydite/mutation.go
  - source/cli/internal/test/run/env.go
  - source/cli/internal/executil/executil.go
targets: cmd-lydite-suite-sits-near-gos-default-timeout
verdict: still-true
---

Adds a verified workaround to the existing timeout candidates
(`20260921-cmd-lydite-suite-sits-near-gos-default-timeout`,
`20260927-cli-components-declared-args-carry-no-timeout`,
`20260927-mutation-cli-baseline-hits-go-tests-default-timeout`).

`.lydite/components.yml` declares `cli` with `runner: go-test`, `args: ["-race", "./..."]` — no
`-timeout`. The `cmd/lydite` test package took ~18–20 minutes on a loaded Mac during the
`refactor/flow-record` work, over `go test`'s default 10 minutes, so
`lydite mutation --component cli` locally answers the unmeasured row "the baseline suite did not
pass, so nothing can be concluded about what a mutant would change"
(`cmd/lydite/mutation.go`, after `executil.RunOutput` of the baseline), with a
`panic: test timed out after 10m0s` in the tail and gotestsum listing every in-flight test with an
`(unknown)` outcome — not real failures.

Workaround: run with `GOFLAGS=-timeout=30m` in the environment. The baseline's child environment
is `ChildEnv` (`internal/test/run/env.go`) layered by `executil` onto `os.Environ()`
(`internal/executil/executil.go`, `cmd.Env = append(os.Environ(), extraEnv...)`), so `GOFLAGS`
reaches the `go test` gotestsum spawns; `go test` honours `-timeout` from `GOFLAGS` (checked with a
3s-sleep test under `GOFLAGS=-timeout=1s`: it panics at 1s). No explicit `-timeout` is in the
declared args to override it. This leaves the declaration untouched, which matters because
changing `args:` is not free: the go-test producer string folds in scope args only (see
`.claude/rules/fold-only-denominator-moving-args-into-a-producer.md`), so `-timeout` would not move
the baseline, but it is a repository-wide declaration change for a local-machine problem.

Caveat: a measured local run under that load is not a substitute for CI's. On the record-flow
branch the local run (load ~90 on 8 cores) reported 7 of 95 survivors, all in
`internal/stages/record`; CI's run of the same tree found two more in `cmd/lydite/record.go` that
the local run had counted killed. A mutant whose suite runs past the per-mutant timeout counts as
killed (`internal/mutation/executor_test.go`'s `TestASuiteThatHangsTimesOutAndCountsAsKilled`),
and a `./cmd/lydite` mutant under heavy load is the likeliest to — so a contended local run
over-reports kills in exactly the slowest package. Inferred from the two runs' difference, not
reproduced mutant by mutant.
