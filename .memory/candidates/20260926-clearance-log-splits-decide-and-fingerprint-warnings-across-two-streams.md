---
about: cmd/lydite/clearance.go's logClearance writes Decide-stage warnings (and reply failures) to os.Stderr but Fingerprint-stage warnings to cmd.ErrOrStderr() — on purpose, because clearance_test.go asserts each on a different stream; unifying them breaks tests
saw:
  - source/cli/cmd/lydite/clearance.go
  - source/cli/cmd/lydite/clearance_test.go
  - source/cli/cmd/lydite/mutation_test.go
  - source/cli/internal/stages/clearance/clearance.go
  - source/cli/internal/stages/clearance/fingerprint.go
---

`logClearance` (`cmd/lydite/clearance.go`) writes three kinds of job-log line after the flow runs:

- `DecideOut.Warnings` (the "lydite: deriving the change's uncovered paths: ..." line from
  `clearancestages.Decide`) → `os.Stderr`
- `FingerprintOut.Warnings` (e.g. "... records no fingerprint ...", the title-lookup warning)
  → `cmd.ErrOrStderr()`
- each `r.Errors()` reply failure ("lydite: the decision was recorded but the reply was not
  posted: ...") → `os.Stderr`

The split is inherited from `main`'s pre-Flow `cmd/lydite/clearance.go`, which already used
`os.Stderr` for the derivation and reply-failure lines and `cmd.ErrOrStderr()` for the
fingerprint lines. It is load-bearing in tests, not in production (in production
`cmd.ErrOrStderr()` *is* `os.Stderr`):

- `runClearanceWith` (`clearance_test.go`) sets `cmd.SetOut` and `cmd.SetErr` to one buffer and
  returns it; `TestAClearanceRecordsNoFingerprintOffTheRevisionItClears` asserts
  `"records no fingerprint"` in that buffer — so Fingerprint warnings must go to the command's
  writer.
- `TestAFailedDerivationIsNamedInTheJobLog`, `TestAnUnparseableExemptionsFileIsRefused`,
  `TestAProposalThatWorkedLogsNothing` and `TestAReplyThatCannotBePostedIsLoggedAndFailsNothing`
  wrap the run in `capturedStderr` (`mutation_test.go`), which swaps the process-wide
  `os.Stderr` for a pipe — so derivation warnings and reply failures must go to `os.Stderr`.

Routing everything to either stream breaks one group. Unifying them means changing the tests
too: e.g. send all three to `cmd.ErrOrStderr()` and assert on `runClearanceWith`'s buffer
instead of `capturedStderr`. Note `TestAProposalThatWorkedLogsNothing` asserts `os.Stderr` is
*empty*, so anything newly routed to `os.Stderr` on a successful `/lydite exempt` fails it.
