---
about: scheduler.Item has two constructions, so a new lock field must be added to both, and one test holds them together
saw:
  - source/cli/internal/test/run/run.go
  - source/cli/cmd/lydite/test.go
  - source/cli/cmd/lydite/plan.go
  - source/cli/cmd/lydite/plan_test.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/internal/stages/mutation/budget.go
  - source/cli/internal/scheduler/scheduler.go
---

`testrun.ItemFor` (`internal/test/run/run.go` line ~228, wrapped as `itemFor` in
`cmd/lydite/test.go` line ~398 — what a run locks on) and `planItems` (`cmd/lydite/plan.go`
line ~235, what `lydite test plan` shards by) each build a `scheduler.Item`. `Conflicts` is one
predicate, but the planner's copy feeds `shardsOf`; a field carried by only one of them makes the
run and the matrix disagree, and the disagreement is the unsafe direction — CI splits a contending
pair across jobs while nothing serialises them. `TestPlanAndRunSeeTheSameConflicts`
(`plan_test.go` line ~280) builds one declaration through both and compares
`scheduler.Conflicts`; dropping `Occupies` from `ItemFor` fails it.

`lydite mutation` inherits `itemFor` for its per-component scheduling: the CLI's
`mutationLifecycle.Plan` (`cmd/lydite/mutation.go` line ~343) sets each `Planned.Item` to
`itemFor(p)`, and `internal/stages/mutation/run.go`'s `RunMutants` hands those items to
`scheduler.Run` unchanged. Inside a component, `workersFor`
(`internal/stages/mutation/budget.go` line ~65) deliberately builds a ports-only synthetic pair of
items with no directory, which `Item.occupied()` treats as empty, so two mutants of one component
conflict exactly when it publishes a port.
