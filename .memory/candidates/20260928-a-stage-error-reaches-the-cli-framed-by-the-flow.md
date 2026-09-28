---
name: a-stage-error-reaches-the-cli-framed-by-the-flow
kind: gotcha
about: source/cli/internal/flow/run.go
saw: A FailFlow stage's error comes back from flow.Run as *flow.StageError, whose Error() prefixes `flow "<flow>": stage "<stage>": `; a command that returns it unchanged changes its user-facing error text, and a strings.Contains test still passes.
---

`flow.Run` returns a `FailFlow` stage's error wrapped in `*flow.StageError`
(`internal/flow/run.go`, `StageError.Error` near line 112), whose text is
`flow "<flow>": stage "<stage>": <the stage's own error>`. A command moved onto a flow
must unwrap it — `errors.As(err, &stageErr)` then `stageErr.Err`, falling through to `err`
otherwise — before returning or comparing it, or every error it reports gains that prefix.
`cmd/lydite/release.go`'s `releaseError` and `cmd/lydite/clearance.go`'s `clearanceError`
both do this, and apply a sentinel translation (`releasestages.ErrNoTag`,
`ErrNoCredential`) only after the unwrap.

The prefix is invisible to a test asserting with `strings.Contains`, because the stage's
own text is still a substring. Only an exact comparison (`err.Error() == want`) catches it —
`TestReleaseCheckErrorsCarryNoFlowFraming` in `cmd/lydite/release_test.go` asserts that for
the no-tag, shallow-checkout and unreadable-range errors.
