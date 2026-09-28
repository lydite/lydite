---
about: flow.Run checks ctx.Err() before each stage and returns it bare, so a command moved onto a flow reports "context canceled" on cancellation where its hand-written body reported the in-flight call's own error
saw:
  - source/cli/internal/flow/run.go
  - source/cli/cmd/lydite/mergequeue.go
---

`flow.Run` tests `ctx.Err()` at the top of every stage iteration and returns it unwrapped — not a
`*flow.StageError` (`internal/flow/run.go:194`). A command body that called the same functions in
sequence surfaced whatever the git or HTTP call in flight returned when the context was cancelled;
once the same steps are stages, a cancellation between them surfaces as the bare context error
instead. A CLI that unwraps `*flow.StageError` to report a stage's own error (as
`cmd/lydite/mergequeue.go`'s `queueError` does) passes this one through unchanged, since it is
not a `StageError`. Byte-identical-output migrations hold for every path except external
cancellation; a command that must preserve its cancellation text needs its own handling.
