---
name: fold-helpers-do-not-know-about-no-suite-components
kind: gotcha
description: componentRows and componentRowsNoting flag a component absent from every shard, but a no-suite component is deliberately in none, so each fold command needs its own no-suite wrapper (suiteRows, mutationRows).
anchors:
  - path: source/cli/cmd/lydite/fold.go
    blob: 454c64d57793
  - path: source/cli/cmd/lydite/merge.go
    blob: 4f0a5cea19eb
  - path: source/cli/cmd/lydite/mutation_merge.go
    blob: 94a878f61893
confidence: verified
---

`fold.go`'s `componentRowsNoting`/`componentRows` iterate every component in the declaration and report "<name> has no row in any shard's report" for one no shard mentions. They do not know a component can legitimately take no row: a no-suite component (ADR 0056, `declaresNoSuite` in `test.go`) is deliberately placed in no shard by `plan.go`'s `planItems`, so its absence is the plan working, not a dead shard.

Every fold command calling these helpers must special-case `declaresNoSuite` itself before handing the rest of the declaration to the shared helper — the helper should not learn the rule, since a stray shard row for a no-suite component (e.g. from `--component`) must still not be silently trusted. `merge.go`'s `suiteRows` does this for the test/flaky fold; `mutation_merge.go` has its own `mutationRows` (`:168`) mirroring it. When `mergeMutationShards` first gained no-suite support (lydite/lydite#252) it called `componentRowsNoting` directly and broke `lydite mutation merge` for any repo declaring a no-suite component — caught only by manually testing the merge path. A third fold command reading `decl.Components` through these helpers must write its own wrapper; nothing enforces it and tests without a no-suite fixture pass.
