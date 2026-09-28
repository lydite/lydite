---
description: "A stage that performs more than one write reports how far it got, on failure, through a typed error implementing Unwrap — never through Out, which a failed stage has none of."
---

# Carry a multi-write stage's progress in a typed error, never in `Out`

`flow.Output[T]` is `flow.ErrUnavailable` once a stage has failed, so a stage that performs more
than one write per call — deleting several comments, posting a review and then several file
comments — cannot report how far it got before failing by writing further into its own `Out`:
there is no `Out` a caller can read back. `threadsstages.TakeDown` and `threadsstages.Open` each
carry that progress in a typed error instead — `*TakeDownError{Answered []int64, Err error}` names
every comment answered instead of deleted before the failure; `*OpenError{Lost, Posted int, Err
error}` names how many claims reached the pull request and how many did not. Both implement
`Unwrap`, so `errors.Is`/`errors.As` still reach the underlying cause, and both are read with
`errors.As` at the call site when `flow.Output` itself comes back unavailable.

Losing that progress silently is the failure this avoids: a caller that only sees "the stage
failed" cannot warn about the comments it did answer, or count the claims that did land before
the error — exactly what a reader investigating the failure needs, and what render-time behavior
(a warning printed for each answered comment, a row naming how many claims reached no surface)
depends on being able to read back.

## Applies to

Any stage, in `threadsstages` or a future stage package, whose single call performs more than one
write and can fail partway through.

## Example

```go
// wrong: a failed stage has no Out, so partial progress is simply lost
func Open(ctx context.Context, in OpenIn) (OpenOut, error) {
    if err := in.Repository.CreateReview(ctx, in.Ref.Number, in.Ops.Head, in.Ops.Create); err != nil {
        return OpenOut{}, err // how many of in.Ops.Create landed is gone
    }
    // ...
}

// right: the progress travels in a typed error a caller can unwrap
type OpenError struct {
    Lost, Posted int
    Err          error
}

func (e *OpenError) Unwrap() error { return e.Err }

func Open(ctx context.Context, in OpenIn) (OpenOut, error) {
    if err := in.Repository.CreateReview(ctx, in.Ref.Number, in.Ops.Head, in.Ops.Create); err != nil {
        return OpenOut{}, &OpenError{Lost: len(in.Ops.Create), Posted: 0, Err: err}
    }
    // ...
}
```

Reasoning: [`agentic/references/architecture.md`](../references/architecture.md#the-threads-flow) and
[`docs/adr/0073-threads-declares-trust-first-and-writes-only-what-it-listed.md`](../../docs/adr/0073-threads-declares-trust-first-and-writes-only-what-it-listed.md).
