---
about: package dependency direction between internal/component and internal/runner
saw:
  - source/cli/internal/component/component.go
  - source/cli/internal/runner/runner.go
  - source/cli/internal/test/run/run.go
  - source/cli/internal/test/measure/record.go
  - source/cli/internal/stages/record/baseline.go
---

`internal/component` imports `internal/runner` (to reference `runner.Name` etc.), so
`internal/runner` cannot import `internal/component` back without an import cycle
(`runner.go`'s own comment near `goScope` says so: "internal/component imports this package").
This rules out plumbing a new component-schema field (e.g. a hypothetical named `coverpkg:` key)
directly into `buildGoTest` or any other `internal/runner` function without first widening
`Runner.Build`'s signature (currently `func(variant Variant, args []string) (Invocation, bool)`)
and updating every caller. The production callers are `internal/test/run/run.go` (the variant a
component's suite runs as), `internal/test/measure/record.go` (the instrumented report path), and
`internal/stages/record/baseline.go`'s `UnmeasurableByDeclaration` (whether a declaration can ever
be measured), plus direct calls in `cmd/lydite/test_test.go` and
`internal/mutation/overlay_test.go`. (`internal/flaky`'s `Options.Build` is a different function
type — scope and tests to an invocation — wired by `internal/test/run/flaky.go`, not a
`Runner.Build` call.)

A schema-level decision (like whether to add a named key for coverage-scope narrowing, see
`[[gotest-coverpkg-last-flag-wins-over-component-args]]`) that only needs to be *read* inside
`internal/runner` is therefore not a cheap addition — it is a signature change with several call
sites to update, which was decisive in choosing to keep `args:` as the mechanism rather than
adding a `coverpkg:` schema key (lydite/lydite#185).
