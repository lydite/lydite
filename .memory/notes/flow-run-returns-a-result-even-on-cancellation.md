---
name: flow-run-returns-a-result-even-on-cancellation
kind: gotcha
description: flow.Run returns a bare context error on cancellation but a Result still carrying completed stages' output, so a caller treating any error as fatal renders no report at all.
anchors:
  - path: source/cli/internal/flow/run.go
    blob: 0e4ecd236760
  - path: source/cli/cmd/lydite/test.go
    blob: 9be41f98c783
  - path: source/cli/internal/flows/test/test_test.go
    blob: 77dfc5d38bae
confidence: suspect
---

`(*flow.Flow).Run` (`internal/flow/run.go`) checks `ctx.Err()` before each stage and returns a bare `context.Canceled`/`DeadlineExceeded` (not a `*flow.StageError`) when it finds one, but the `*flow.Result` it also returns still holds every completed stage's output. The first cut of `lydite test`'s `RunE` returned immediately on any flow error, so a SIGINT/SIGTERM mid-run (or a CI timeout) printed a bare `context canceled` and rendered nothing — no rows, no `.lydite-reports/test.json` — where the pre-Flow engine rendered a partial report with a failing schedule row and exited 1 (#270). That is the "section that quietly disappears reads as passed" failure in its worst form.

The fix reads the error and the Result together (`cmd/lydite/test.go:231` checks `errors.Is(err, context.Canceled)`/`DeadlineExceeded`): a `*flow.StageError` (cancellation inside a stage included) is a real failure; a bare cancellation with the last relevant stage's `r.Status(name)` still `flow.StatusSucceeded` means the flow stopped *between* stages and what that stage reported is the honest partial answer to render; a cancellation before the first relevant stage leaves nothing to render. A stage never reached reports `flow.StatusNotReached` (`run.go:79-81`), and `flow.Output[T]` on such a stage is itself an error, so a caller wanting "output or nothing" needs its own zero-value fallback keyed on `Status`. Only the cancellation check at `test.go:231` and the status constant were re-read here, hence `suspect`.
