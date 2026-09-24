---
about: componentRowsNoting/componentRows in fold.go and the no-suite component shape
saw: source/cli/cmd/lydite/fold.go, merge.go, mutation_merge.go
---

`fold.go`'s `componentRowsNoting`/`componentRows` iterate every component in the declaration and
report a problem ("<name> has no row in any shard's report") for one no shard mentions. They have
no idea a component can legitimately take no row at all — a no-suite component (ADR 0056,
`declaresNoSuite` in `test.go`) is deliberately placed in no shard by `plan.go`'s `planItems`, so
it will never appear in any shard's report, and that absence is the plan working correctly rather
than a dead shard.

Every fold command that calls these helpers has to special-case `declaresNoSuite` itself, before
handing the rest of the declaration to the shared helper — the helper does not and should not
learn this rule, since a stray shard row for a no-suite component (e.g. from `--component`) still
must not be silently trusted. `merge.go`'s `suiteRows` does this for the test/flaky fold. When
`mutation_merge.go`'s `mergeMutationShards` was first changed to support no-suite components in
lydite/lydite#252, it called `componentRowsNoting` directly for the mutation label and was missed
— it broke `lydite mutation merge` for any repo declaring a no-suite component, caught only by
manually testing the merge path rather than by `go test`. `mutation_merge.go` now has its own
`mutationRows` wrapper mirroring `suiteRows`'s pattern exactly.

**Anything that adds a third fold command reading `decl.Components` through `componentRowsNoting`/
`componentRows` must write its own no-suite wrapper too** — there is no single point that enforces
this, and it will fail silently in tests that don't declare a no-suite component in their fixture.
