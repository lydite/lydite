---
about: scheduler.Item has two constructions, so a new lock field must be added to both, and one test holds them together
saw:
  - source/cli/cmd/lydite/test.go
  - source/cli/cmd/lydite/plan.go
  - source/cli/cmd/lydite/plan_test.go
  - source/cli/internal/scheduler/scheduler.go
---

`itemFor` (test.go, what a run locks on) and `planItems` (plan.go, what `lydite test plan` shards
by) each build a `scheduler.Item`. `Conflicts` is one predicate, but the planner's copy feeds
`shardsOf`; a field carried by only one of them makes the run and the matrix disagree, and the
disagreement is the unsafe direction — CI splits a contending pair across jobs while nothing
serialises them. `TestPlanAndRunSeeTheSameConflicts` (plan_test.go) builds one declaration through
both and compares `scheduler.Conflicts`; dropping `Occupies` from `itemFor` fails it. `mutation.go`
inherits `itemFor` for its per-component scheduling, and its `workersFor` deliberately builds a
ports-only synthetic item with no directory, which `Item.occupied()` treats as empty.
