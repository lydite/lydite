---
name: cli-suite-outruns-gos-default-test-timeout
kind: gotcha
description: The cli component declares go-test args with no -timeout and its cmd/lydite package can outrun go test's 10-minute default on a loaded machine, so lydite's own test and mutation baseline die on a timeout panic with no failing test.
anchors:
  - path: .lydite/components.yml
    blob: de5da43c2cdf
  - path: source/cli/internal/stages/mutation/run.go
    blob: f2d6f052cb32
  - path: source/cli/cmd/lydite/mutation.go
    blob: 3ad21f631005
  - path: source/cli/internal/test/run/env.go
    blob: 833cfbed2a5e
  - path: .github/workflows/ci-test.yml
    blob: c425a45dac8b
confidence: suspect
---

`.lydite/components.yml` declares `cli` as `runner: go-test`, `dir: source/cli`, `args: ["-race", "./..."]` — no `-timeout` — and `.github/workflows/ci-test.yml` sets none either. Measured in earlier sessions, the `cmd/lydite` test package took 353-588s on a quiet machine and 15-23 minutes on a loaded one under `-race`, so `go test`'s 10m default panics with no `--- FAIL` line and every in-flight test showing an `(unknown)` outcome. That is the package being slow, not a regression to bisect. The timings were not reproduced when this note was written, hence `suspect`; the declaration and the missing flags were re-read.

**Effect on `lydite mutation --component cli`.** `mutateComponent` (`internal/stages/mutation/run.go`, ~`:358`) runs the baseline as the Instrumented variant; when that suite dies on the timeout the component becomes `KindBaselineFailed` (`run.go:58-60`, `:102`) and `cmd/lydite/mutation.go`'s `kindRow` renders the unmeasured row "the baseline suite did not pass, so nothing can be concluded about what a mutant would change". `lydite mutation --timeout` bounds each mutant's suite, not the baseline (`internal/stages/mutation/budget.go` applies only after the baseline passes).

**Workaround leaving the declaration alone:** `GOFLAGS=-timeout=30m` in the calling environment. The baseline's child environment is `ChildEnv` (`internal/test/run/env.go`) layered onto `os.Environ()` by `internal/executil/executil.go`, and `go test` honours `-timeout` from `GOFLAGS`. Do not add `-timeout` to `args:` — see [[go-buildonly-passes-go-test-only-flags-to-go-build]]. Hand-typed verification commands should pass `-timeout 30m`.

**A contended local run over-reports kills.** A mutant whose suite runs past the per-mutant timeout counts as killed (`TestASuiteThatHangsTimesOutAndCountsAsKilled`, `internal/mutation/executor_test.go`), so a loaded local run counted 7 of 95 survivors where CI's run of the same tree found two more. Inferred from the difference, not reproduced mutant by mutant. Part of local slowness is GPG-signed fixture commits: [[cmd-lydite-fixtures-inherit-global-git-signing]].
