---
name: component-lang-is-empty-for-command-and-lang-only-components
kind: gotcha
description: Component.Lang() answers empty for an unknown runner, a command component and a lang-only component alike; the scan side reads ScanLang() instead.
anchors:
  - path: source/cli/internal/component/component.go
    blob: 78b9519abbc5
  - path: source/cli/internal/stages/scan/plan.go
    blob: 6e5358165f49
  - path: source/cli/internal/orphan/orphan.go
    blob: 35f44afe37d8
confidence: verified
---

`Component.Lang()` (`internal/component/component.go:204`) looks `c.Runner` up via `runner.Lookup` and returns `""` on a miss. A component with no `runner:` never sets `Runner`, so `Lang()` answers `""` for a `command:` component and a `lang:`-only one just as it does for an unrecognised runner name — three different shapes, one answer. `validateAPISurface` therefore switches on `c.Lang()` with `case runner.Go, runner.Rust, runner.TypeScript: return nil` and sends every other answer (unset runner, `command:`, declared `lang:`) to the same `default` error; it does not rely on `validateInvocation` having run first.

ADR 0056 decided a declared `lang:` (`Component.DeclaredLang`) does NOT reach `Lang()` or its readers, because they assume a non-empty answer came from a runner. `Component.ScanLang()` (`component.go:214`) is the separate accessor — the runner's language when a runner is set, `DeclaredLang` otherwise — read only by the scan side: `PlanComponents` (`internal/stages/scan/plan.go`), `scanUnits`/`anyLanguageDeclared` (`internal/stages/scan/toolchains.go`), `FindingCounts` (`internal/stages/record/findings.go`) and `internal/orphan/orphan.go`.
