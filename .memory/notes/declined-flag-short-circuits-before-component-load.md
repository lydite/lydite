---
name: declined-flag-short-circuits-before-component-load
kind: invariant
description: lydite mutation --declined is checked first in RunE, before the mutation flow is built or run, so a repository can decline the whole mutation concern even when its .lydite/components.yml would fail to load — a declined run touches no component at all.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 3ad21f631005
  - path: source/cli/cmd/lydite/mutation_test.go
    blob: 17a556c497c0
confidence: suspect
---

Added by ADR 0044. In `newMutationCmd`'s `RunE` the flag adds exactly one `ui.Row{Status:
ui.StatusDeclined, ...}` to a fresh report, renders it, and returns — before the interrupt handler
is installed, before `--concurrency`/`--timeout`/`--memory` are parsed, and before
`mutationflow.New()` is built or run. None of the flow's stages, component planning, toolchain
resolution or compose services ever run. `TestADeclinedRunWritesOneRowAndTouchesNoComponent`
(`mutation_test.go`) declares a component with a nonexistent `dir` that `component.Load` would
refuse, and confirms the declined run still succeeds. Anything that reorders `RunE`'s early checks
in this command must keep `--declined` ahead of every other one, or a repository trying to decline
mutation because its declaration is broken would fail for the very reason it was declining.
</content>
