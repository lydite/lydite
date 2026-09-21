---
about: package dependency direction between internal/component and internal/runner
saw: source/cli/internal/component/component.go, source/cli/internal/runner/runner.go
---

`internal/component` imports `internal/runner` (to reference `runner.Name` etc.), so
`internal/runner` cannot import `internal/component` back without an import cycle. This rules
out plumbing a new component-schema field (e.g. a hypothetical named `coverpkg:` key) directly
into `buildGoTest` or any other `internal/runner` function without first widening
`Runner.Build`'s signature (currently `func(variant Variant, args []string)`) and updating
every caller: `cmd/lydite/test.go`, `cmd/lydite/record.go`, `internal/flaky/rerun.go`, plus
mutation and staging tests. A schema-level decision (like whether to add a named key for
coverage-scope narrowing, see `[[gotest-coverpkg-last-flag-wins-over-component-args]]`) that
only needs to be *read* inside `internal/runner` is therefore not a cheap addition — it is a
signature change with several call sites to update, which was decisive in choosing to keep
`args:` as the mechanism rather than adding a `coverpkg:` schema key (lydite/lydite#185).
