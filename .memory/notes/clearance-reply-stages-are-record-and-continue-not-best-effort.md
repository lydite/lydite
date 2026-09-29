---
name: clearance-reply-stages-are-record-and-continue-not-best-effort
kind: rationale
description: The clearance reply stages use RecordAndContinue rather than BestEffort so the CLI can word the failure from Result.Errors(); BestEffort would send it to a log the flow never sets.
anchors:
  - path: source/cli/internal/flow/run.go
    blob: 0e4ecd236760
  - path: source/cli/internal/flows/clearance/clearance.go
    blob: f485bdb9be4f
  - path: source/cli/cmd/lydite/clearance.go
    blob: 65506735cacb
confidence: verified
---

Both reply stages of the clearance flow (`reply-clearance` and `reply`, each `scmstages.PostComment`) are `.OnError(flow.RecordAndContinue)` (`internal/flows/clearance/clearance.go:206` and `:219`). Both non-failing policies keep the run going and leave the stage's output unavailable; they differ in where the error goes (`internal/flow/run.go`, the policy switch at ~`:214`):

- `RecordAndContinue` appends a `*flow.StageError` to the `Result`, readable via `Result.Errors()` — the only policy whose errors `Errors()` holds.
- `BestEffort` does `fmt.Fprintln(f.log, serr)` and nothing else, in the engine's framing (`flow "clearance": stage "reply": <err>`). The log is whatever `Builder.Log` set, defaulting to `io.Discard` — and the clearance flow never calls `Log`, so a reply failure would vanish entirely.

`logClearance` (`cmd/lydite/clearance.go:180`) ranges over `r.Errors()` and prints `lydite: the decision was recorded but the reply was not posted: <failed.Err>` to `os.Stderr` — the inner error, not the framing. `TestAReplyThatCannotBePostedIsLoggedAndFailsNothing` asserts that prefix for both `/lydite clear` and `/lydite explain`; switching to `BestEffort` fails it. Rule: `RecordAndContinue` when the caller must word, count or react to a non-fatal stage failure; `BestEffort` only for failures nothing downstream needs beyond a raw log line.
