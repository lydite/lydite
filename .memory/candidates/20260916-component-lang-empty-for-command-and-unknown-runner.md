---
about: Component.Lang() returns "" for an unknown runner, a command-invoked component and a lang-only component alike, so a validation checking "this component declares no language" must call Lang() rather than assume Runner is set; the scan path reads ScanLang() instead
saw:
  - source/cli/internal/component/component.go
  - source/cli/internal/stages/scan/plan.go
  - source/cli/internal/stages/scan/toolchains.go
  - source/cli/internal/stages/record/findings.go
  - source/cli/internal/orphan/orphan.go
  - docs/adr/0056-a-component-states-its-language-only-where-no-runner-implies-one.md
---

`Component.Lang()` (`internal/component/component.go`) looks up `c.Runner` via `runner.Lookup`
and returns `""` on a miss. A component with no `runner:` never sets `Runner` at all, so `Lang()`
answers `""` for a `command:`-invoked component and for a `lang:`-only one too — the same empty
answer as an unrecognised runner name, even though those are different shapes. `validateInvocation`
requires at least one of `runner`, `command` or `lang`, refuses `runner` beside `command` and
`runner` beside `lang`, and refuses an unknown runner name — so after validation a component with
`Runner == ""` is a command component, a lang-only component, or a command component that also
declares `lang:`.

`validateAPISurface` switches on `c.Lang()` with `case runner.Go, runner.Rust, runner.TypeScript:
return nil` and every other answer — an unsupported language, an unset runner, `command:` or a
declared `lang:` — falling to the same `default` error. It does not rely on `validateInvocation`
having run first in the same validation loop.

ADR 0056 decided a declared `lang:` (`Component.DeclaredLang`) does NOT reach `Lang()` or its
readers, because they assume a non-empty answer came from a runner. `Component.ScanLang()` is the
separate accessor (the runner's language when a runner is set, `DeclaredLang` otherwise), read only
by the scan side: `PlanComponents` (`internal/stages/scan/plan.go`), `scanUnits` and
`anyLanguageDeclared` (`internal/stages/scan/toolchains.go`), `FindingCounts`
(`internal/stages/record/findings.go`) and `internal/orphan/orphan.go`.
