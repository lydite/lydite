---
about: tests that make a file unreadable or read-only with chmod fail when the suite runs as uid 0, because root can still read and write it
saw:
  - source/cli/cmd/lydite/clearance_test.go
  - source/cli/internal/ledger
---

`TestAnUnreadableExemptionsFileIsRefused` (cmd/lydite) and the ledger's `TestAppendReportsAWriteItCouldNotMake` and `TestAnUnreadablePartitionIsAnErrorRatherThanAMiss` fail under uid 0: `chmod 0o000` succeeds, so the test's own skip never fires, and the file stays readable. A test needing an unreadable path can use a path under a regular file instead, as `TestScopeChangeSwitchesResumeOffWhenTheStateRootCannotBeCreated` does.
