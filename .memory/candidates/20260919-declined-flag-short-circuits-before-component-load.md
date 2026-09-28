---
name: declined-flag-short-circuits-before-component-load
kind: invariant
about: source/cli/cmd/lydite/mutation.go
description: lydite mutation --declined is checked first in RunE, before the mutation flow is built or run, so a repository can decline the whole mutation concern even when its .lydite/components.yml would fail to load (an unbuildable component, a bad declaration) — a declined run touches no component at all.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 42d44e167d073f970c70ff265aca2516e1bda35f
  - path: source/cli/cmd/lydite/mutation_test.go
    blob: a7e20baebafd0e103ba7c9e99efdcff075ea535c
confidence: verified
---

Added by ADR 0044. In `newMutationCmd`'s `RunE` (`cmd/lydite/mutation.go` line ~84) the flag
adds exactly one `ui.Row{Status: ui.StatusDeclined, Label: "mutation", Value: "declined for
this run"}` to a fresh `ui.NewReport("mutation")`, calls the same `renderReport` helper every
other path in this command uses, and returns — before the interrupt handler is installed,
before `--concurrency`/`--timeout`/`--memory` are parsed, and before `mutationflow.New()` is
built or run. None of the flow's stages (`LoadDeclaration`, `ProvisionToolchains`,
`ResolveBase`, `SelectAffected`, `ScopeChange`, `RunMutants`), component planning, toolchain
resolution or compose services ever run. This is intentional and tested
(`TestADeclinedRunWritesOneRowAndTouchesNoComponent` in `mutation_test.go` declares a component
with a nonexistent `dir` that `component.Load` would refuse, and confirms the declined run still
succeeds). Anything that reorders `RunE`'s early checks in this command must keep `--declined`
ahead of every other one, or a repository trying to decline mutation because its declaration is
broken would fail for the very reason it was declining.
