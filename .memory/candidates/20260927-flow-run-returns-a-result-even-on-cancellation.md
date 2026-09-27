---
about: (*flow.Flow).Run checks ctx.Err() before every stage and returns a bare context.Canceled/context.DeadlineExceeded (not wrapped in a *flow.StageError) the moment it finds one, but the *flow.Result it also returns still carries every already-completed stage's output; a caller that treats any non-nil error as fatal discards a partial run's real output
saw:
  - source/cli/internal/flow/run.go
  - source/cli/cmd/lydite/test.go
  - source/cli/internal/flows/test/test_test.go
targets: null
verdict: null
---

Found fixing an interrupted-run regression while migrating `lydite test` onto Flow (#270): the
first cut of the rewritten `RunE` returned immediately on any error from running the flow,
which meant a SIGINT/SIGTERM mid-run (or a CI job's timeout) printed a bare `context canceled`
and rendered nothing — no rows, no `.lydite-reports/test.json` — where the pre-migration engine
rendered a partial report with a failing schedule row and exited 1. That is the specific failure
this repo's own rule against a section that quietly disappears reading as one that passed exists
to catch, in its worst form: not a mis-rendered section but no report at all.

The fix distinguishes three cases by reading both the error and the `*flow.Result`:
- `errors.As(err, &*flow.StageError)` — a stage's own error, cancellation included, is a real
  failure and must still return as one.
- `errors.Is(err, context.Canceled)`/`context.DeadlineExceeded` with the last-relevant stage's
  own `r.Status(stageName)` still `flow.StatusSucceeded` — the flow stopped *between* stages,
  and whatever that stage already reported is the honest partial answer to render.
- Otherwise (cancelled before the first relevant stage even ran) — there is nothing to render,
  and the bare cancellation is correct to return.

A stage the flow never reached reports `flow.StatusNotReached` (`internal/flows/test/test_test.go`
exercises this directly with an immediately-cancelled context) and `flow.Output[T]` on such a
stage is itself an error — a caller wanting "this stage's output, or nothing, without erroring"
needs its own zero-value fallback keyed on `Status`, which the Flow engine does not provide out
of the box.
