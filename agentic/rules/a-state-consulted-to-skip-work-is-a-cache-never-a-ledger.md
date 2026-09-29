---
description: "A state consulted only to skip work is a Cache: a read or write failure is a diagnostic, never a failed run or a fabricated verdict."
---

# A state consulted to skip work is a Cache, never a Ledger

`internal/mutation`'s state holds verdicts a rerun does not need to measure again. Everything in
it can be reproduced by running the suite, so losing it costs time and nothing else. Every
failure touching it — it cannot be opened, a line cannot be read, a write fails, the disk is
full — is therefore reported as a diagnostic and the run carries on as if there were no state: it
measures what it has no verdict for. A failure must never fail the run, and must never stand in
for a verdict: a mutant whose recorded verdict could not be read is unmeasured, not killed and
not survived. Anything that resumes work by consulting recorded results is held to the same
line; a record that cannot be recomputed is a Ledger, and belongs to a different rule.

## Applies to

`source/cli/internal/mutation`'s `State` and its callers in `internal/stages/mutation`, and any
later code that resumes a run from what an earlier one recorded.

## Example

```go
// wrong: a state that cannot be read fails the run, or reads as a verdict
verdicts, err := st.Verdicts()
if err != nil {
    return ComponentOutcome{}, err
}

// right: the failure is reported and the mutants are measured
verdicts, err := st.Verdicts()
if err != nil {
    fmt.Fprintf(diagnostics, "mutation state unreadable, measuring everything: %v\n", err)
    verdicts = nil
}
```

Reasoning: [ADR 0075](../../docs/adr/0075-a-mutation-run-resumes-and-stops-at-a-deadline.md) and
[`agentic/references/mutation.md`](../references/mutation.md).
