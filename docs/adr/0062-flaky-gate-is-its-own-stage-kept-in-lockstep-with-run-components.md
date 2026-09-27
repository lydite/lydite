# The flaky gate is its own Flow stage, kept in lockstep with RunComponents rather than fused into it

Before the Flow migration, `newFlakyGate`/`examine`/`run` (`cmd/lydite/test.go:1516-1927`)
examined each component's flakiness inline, inside the same per-component pass that ran its
main suite — not as a separate pass over already-run components. The Flow migration for `test`
(#270) splits this into its own stage, `FlakyGate`, running after `RunComponents`, matching the
per-concern stage granularity the rest of the flow uses (one stage per report section) rather
than folding gate logic into the stage that produces the measurement it gates.

This is a deliberate trade-off, not the obvious shape: the fused original coupled scheduling and
gating so tightly that a naive split changes *when* the gate examines a component relative to
every other component's own run, which risks an observable difference in output ordering or
timing even when every component's final verdict is identical. Splitting them anyway keeps every
other stage's boundary uniform (one stage, one section of the report) and testable in isolation
at the cost of an invariant the two stages must maintain by hand: `FlakyGate`'s `In` carries
exactly the ordered, resolved component list and measurements `RunComponents` produced, and
`FlakyGate` must reproduce the fused code's examination order and stderr/log interleaving from
that alone — verified by diffing `test`'s stdout/stderr/report bytes against the pre-migration
command, not merely by comparing final pass/fail verdicts.

## Consequences

A later change that reorders `RunComponents` and `FlakyGate`, or that changes what either stage
writes to shared output streams, must re-verify this byte-identity invariant rather than assume
it holds because the tests pass — a test asserting only the final gate verdict would not catch a
change to interleaving or ordering that altered `--stream` output or log content without changing
any row's status.
