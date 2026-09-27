---
about: `lydite mutation --component cli` cannot produce a baseline on a loaded machine, because the cli component declares args ["-race", "./..."] with no -timeout, so its instrumented baseline run inherits go test's default 10m and the cmd/lydite package exceeds it
saw:
  - .lydite/components.yml
  - source/cli/internal/runner/runner.go
  - source/cli/cmd/lydite/mutation.go
  - .memory/candidates/20260921-cmd-lydite-suite-sits-near-gos-default-timeout.md
---

`.lydite/components.yml` declares the `cli` component as `runner: go-test`, `dir: source/cli`,
`args: ["-race", "./..."]` — no `-timeout`. `mutateComponent` (`cmd/lydite/mutation.go`) runs the
component's baseline as the Instrumented variant, which `buildGoTest`
(`internal/runner/runner.go`) builds as `gotestsum ... -- -coverprofile=... -coverpkg=./...`
followed by the declared args unchanged (`goTestArgs`). Nothing in that argv sets a timeout, so
`go test`'s own default of 10 minutes per test binary applies.

The `cmd/lydite` package's test binary takes 15-23 minutes under `-race` on a loaded machine (see
`20260921-cmd-lydite-suite-sits-near-gos-default-timeout.md`), so it panics at 10m, the baseline
does not pass, and `mutateComponent` returns the `unmeasured` row "the baseline suite did not pass,
so nothing can be concluded about what a mutant would change" — no mutant is generated or run for
the component. The `--timeout` flag on `lydite mutation` bounds each mutant's suite, not the
baseline: its help text says the per-mutant budget is "derived from the component's own baseline
by default".
