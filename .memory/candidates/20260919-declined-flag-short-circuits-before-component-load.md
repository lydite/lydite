---
name: declined-flag-short-circuits-before-component-load
kind: invariant
about: source/cli/cmd/lydite/mutation.go
description: lydite mutation --declined is checked first in RunE, before config.Load/component.Load/anything else, so a repository can decline the whole mutation concern even when its .lydite/components.yml would fail to load (an unbuildable component, a bad declaration) — a declined run touches no component at all.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: ebc4c386f694b85236b66a07b38346ad8028d8a7
confidence: verified
---

Added by ADR 0044. The flag adds exactly one `ui.Row{Status: ui.StatusDeclined, Label:
"mutation", Value: "declined for this run"}` to a fresh `ui.NewReport("mutation")` and
calls the same `renderReport` helper every other path in this command uses, then returns —
none of `prepareMutation`, component planning, toolchain resolution, or compose services
ever run. This is intentional and tested
(`TestADeclinedRunWritesOneRowAndTouchesNoComponent` in mutation_test.go declares a
component with a nonexistent `dir` that `component.Load` would refuse, and confirms the
declined run still succeeds). Anything that reorders `RunE`'s early checks in this command
must keep `--declined` ahead of every other one, or a repository trying to decline mutation
because its declaration is broken would fail for the very reason it was declining.
