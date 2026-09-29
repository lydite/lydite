---
name: clearance-log-splits-warnings-across-two-streams-on-purpose
kind: gotcha
description: logClearance writes Decide warnings and reply failures to os.Stderr but Fingerprint warnings to cmd.ErrOrStderr(), because clearance tests assert each on a different stream; unifying them breaks tests.
anchors:
  - path: source/cli/cmd/lydite/clearance.go
    blob: 65506735cacb
  - path: source/cli/cmd/lydite/clearance_test.go
    blob: c14c733e92ee
confidence: verified
---

`logClearance` (`cmd/lydite/clearance.go`, lines ~171-180) writes three kinds of job-log line after the flow runs: `DecideOut.Warnings` (the "deriving the change's uncovered paths" line) → `os.Stderr`; `FingerprintOut.Warnings` ("... records no fingerprint ...", the title-lookup warning) → `cmd.ErrOrStderr()`; each `r.Errors()` reply failure ("the decision was recorded but the reply was not posted") → `os.Stderr`. The split is inherited from the pre-Flow command. It is load-bearing in tests, not production (there `cmd.ErrOrStderr()` *is* `os.Stderr`):

- `runClearanceWith` sets `cmd.SetOut`/`SetErr` to one buffer; `TestAClearanceRecordsNoFingerprintOffTheRevisionItClears` asserts `"records no fingerprint"` there, so Fingerprint warnings must go to the command's writer.
- `TestAFailedDerivationIsNamedInTheJobLog`, `TestAnUnparseableExemptionsFileIsRefused`, `TestAProposalThatWorkedLogsNothing` and `TestAReplyThatCannotBePostedIsLoggedAndFailsNothing` wrap the run in `capturedStderr` (`mutation_test.go`), which swaps the process-wide `os.Stderr` for a pipe, so derivation warnings and reply failures must go to `os.Stderr`.

Routing everything to either stream breaks one group; unifying means changing the tests too. `TestAProposalThatWorkedLogsNothing` asserts `os.Stderr` is *empty*, so anything newly routed to `os.Stderr` on a successful `/lydite exempt` fails it.
