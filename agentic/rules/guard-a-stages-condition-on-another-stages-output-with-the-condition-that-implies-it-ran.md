# Guard a stage's condition on another stage's output with the condition that implies it ran, declared first

`flow.Run` evaluates a stage's `When`/`Unless` conditions in the order `Builder.When`/`Unless`
declared them, and stops at the first that does not hold — the rest are never evaluated at all.
A condition bound through `flow.FromStage(x, field)` reads `x`'s own output, which is
`flow.ErrUnavailable` whenever `x` was skipped or failed, and reading an unavailable output is not
"skip this stage too": it is a `*StageError` that stops the entire run, regardless of the reading
stage's own `OnError` policy. So a condition on another stage's output is only safe once every
condition that already guarantees that stage ran is declared on *this* stage as well, and declared
*before* it — never alone, and never after.

## Applies to

Any `internal/flows/<command>`'s `New()`, or a future generic flow builder, wiring a `.When`/
`.Unless` chain where one condition is `flow.FromStage` naming a stage that itself runs under a
condition.

## Example

```go
// wrong: reads Decide's own output before confirming Decide ran at all — a
// comment nothing addressed skips "decide", and this stage's own condition
// then fails the whole flow reading its unavailable output
Stage(StageFingerprint, clearancestages.Fingerprint).
    When(clears).
    With("Repository", repository)

// right: the guard that implies "decide" ran is declared, and evaluated, first
Stage(StageDecide, clearancestages.Decide).
    When(addressed).
    With("Repository", repository).
Stage(StageFingerprint, clearancestages.Fingerprint).
    When(addressed).When(clears).
    With("Repository", repository)
```

Reasoning: [`agentic/references/architecture.md`](../references/architecture.md#the-ordering-subtlety).
