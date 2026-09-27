---
about: the clearance flow's two reply stages use flow.RecordAndContinue rather than BestEffort so the CLI can print its own reply-failure line from Result.Errors(); BestEffort would send the error to the flow's log in the engine's framing — and the clearance flow sets no log, so it would be discarded
saw:
  - source/cli/internal/flow/flow.go
  - source/cli/internal/flow/run.go
  - source/cli/internal/flows/clearance/clearance.go
  - source/cli/cmd/lydite/clearance.go
  - source/cli/cmd/lydite/clearance_test.go
  - agentic/references/architecture.md
---

Both reply stages of the clearance flow — `reply-clearance` and `reply`, each
`scmstages.PostComment` — are declared `.OnError(flow.RecordAndContinue)`
(`internal/flows/clearance/clearance.go`). Both non-failing policies keep the run going and leave
the stage's output unavailable; they differ only in where the error goes
(`internal/flow/run.go`, the policy switch in the stage loop):

- `RecordAndContinue` appends a `*flow.StageError` to the `Result`, readable via
  `Result.Errors()` — the only policy whose errors `Errors()` holds.
- `BestEffort` does `fmt.Fprintln(f.log, serr)` and nothing else. `serr.Error()` is the engine's
  framing, `flow "clearance": stage "reply": <err>` (`StageError.Error`). The log is whatever
  `Builder.Log` set, defaulting to `io.Discard` (`internal/flow/flow.go`, `Build`) — and the
  clearance flow never calls `Log`, so under `BestEffort` a reply failure would vanish entirely.

`logClearance` (`cmd/lydite/clearance.go`) ranges over `r.Errors()` and prints
`lydite: the decision was recorded but the reply was not posted: <failed.Err>` to `os.Stderr` —
the inner error, not the flow framing. `TestAReplyThatCannotBePostedIsLoggedAndFailsNothing`
(`cmd/lydite/clearance_test.go`) asserts that exact prefix for both `/lydite clear` and
`/lydite explain`. Switching the stages to `BestEffort` fails that test, and even with a `Log`
wired to stderr the line would carry the engine's wording instead.

General rule this illustrates: pick `RecordAndContinue` whenever the caller must word, count or
react to a non-fatal stage failure; `BestEffort` only for failures nothing downstream needs to
see beyond a raw log line. `agentic/references/architecture.md` states the "decision already
stands" reason for not failing the run, not this reason for which non-failing policy.
