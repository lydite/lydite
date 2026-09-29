---
name: scheduler-item-is-built-twice-and-a-test-pins-it
kind: gotcha
description: scheduler.Item is built by both ItemFor (what a run locks on) and planItems (what the plan shards by), so a new lock field must be added to both; TestPlanAndRunSeeTheSameConflicts holds them together.
anchors:
  - path: source/cli/internal/test/run/run.go
    blob: 8b3f8df80c94
  - path: source/cli/cmd/lydite/plan.go
    blob: 8ab5defe6fed
  - path: source/cli/cmd/lydite/plan_test.go
    blob: f1db955c691f
  - path: source/cli/internal/stages/mutation/budget.go
    blob: 846454c5f813
confidence: verified
---

`testrun.ItemFor` (`internal/test/run/run.go:228`, wrapped as `itemFor` in `cmd/lydite/test.go`) and `planItems` (`cmd/lydite/plan.go:235`, what `lydite test plan` shards by) each build a `scheduler.Item`. `Conflicts` is one predicate, but the planner's copy feeds `shardsOf`; a field carried by only one of them makes the run and the matrix disagree, in the unsafe direction — CI splits a contending pair across jobs while nothing serialises them. `TestPlanAndRunSeeTheSameConflicts` (`plan_test.go:280`) builds one declaration through both and compares `scheduler.Conflicts`; dropping `Occupies` from `ItemFor` fails it.

`lydite mutation` inherits `itemFor` for per-component scheduling. Inside a component, `workersFor` (`internal/stages/mutation/budget.go`) deliberately builds a ports-only synthetic pair of items with no directory, which `Item.occupied()` treats as empty, so two mutants of one component conflict exactly when it publishes a port.
