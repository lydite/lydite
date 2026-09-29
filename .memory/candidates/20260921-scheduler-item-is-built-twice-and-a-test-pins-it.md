---
about: scheduler.Item has two constructions, so a new lock field must be added to both, and one test holds them together
saw:
  - source/cli/internal/test/run/run.go
  - source/cli/cmd/lydite/test.go
  - source/cli/internal/stages/test/plan.go
  - source/cli/cmd/lydite/plan_test.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/internal/stages/mutation/budget.go
  - source/cli/internal/scheduler/scheduler.go
---

`testrun.ItemFor` (`internal/test/run/run.go` line ~228, wrapped as `itemFor` in
`cmd/lydite/test.go` line ~398 — what a run locks on) and `planItems` (unexported,
`internal/stages/test/plan.go` line ~117 as of the `test plan`/`test merge`-onto-Flow branch —
previously `cmd/lydite/plan.go`, what `lydite test plan`'s `GroupShards` stage shards by) each
build a `scheduler.Item`. `Conflicts` is one predicate, but the planner's copy feeds `shardsOf`
(same file, line ~151); a field carried by only one of them makes the run and the matrix
disagree, and the disagreement is the unsafe direction — CI splits a contending pair across jobs
while nothing serialises them. `TestPlanAndRunSeeTheSameConflicts` (`cmd/lydite/plan_test.go`
line ~282) now calls `teststages.GroupShards` rather than `planItems` directly (the CLI-side
symbol was deleted when the command moved onto `testflow.NewPlan()`), flattens the returned
shards' `Conflicts` and compares them, sorted, against `scheduler.Conflicts` built from
`itemFor`; dropping `Occupies` from `ItemFor` still fails it.

`lydite mutation` inherits `itemFor` for its per-component scheduling: the CLI's
`mutationLifecycle.Plan` (`cmd/lydite/mutation.go` line ~343) sets each `Planned.Item` to
`itemFor(p)`, and `internal/stages/mutation/run.go`'s `RunMutants` hands those items to
`scheduler.Run` unchanged. Inside a component, `workersFor`
(`internal/stages/mutation/budget.go` line ~65) deliberately builds a ports-only synthetic pair of
items with no directory, which `Item.occupied()` treats as empty, so two mutants of one component
conflict exactly when it publishes a port.
