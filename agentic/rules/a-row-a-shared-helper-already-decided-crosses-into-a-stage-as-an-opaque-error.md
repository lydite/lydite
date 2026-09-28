# A row a shared helper already decided crosses into a stage as an opaque error

`cmd/lydite`'s `prepare`, `runCommands` and `startServices` already return `(ui.Row, bool)` —
or, for `startServices`, `(func(), ui.Row, bool)` — because `lydite test` decides its own rows
from them directly. A `mutationstages.Lifecycle`
implementation calling one of these cannot hand the decided `ui.Row` across the stage boundary:
a stage's `In`/`Out` never names `internal/ui`'s report type, so the row would have nowhere
type-safe to go. Wrap it instead in a CLI-owned `lifecycleRowError{row}` and return that as a
plain `error`. The stage receiving it never reads it — it is `Lifecycle`'s contract that every
error it returns is a failure whose row the command already decided — and hands it back on
`Planned.NotReady`, a `KindBlocked` outcome, or `RunMutants.TeardownErr`. `lifecycleRow` recovers
the row with `errors.As`, called only for a `KindBlocked` outcome and for a teardown error that
`teardownFailureReplaces` says takes over the row; anything else there falls back to a plain
failing "not runnable" row built from the error's own text, for an error that carries none.
`KindExecuteFailed` — alongside `KindNoBackend`, `KindMeasureFailed` and `KindGenerateFailed` —
never calls `lifecycleRow` at all: `kindRow` renders it as `unmeasuredRow(label, o.Err.Error())`,
the executor's own error text verbatim and never unwrapped.

`lifecycleRowError.Error()` is `strings.Join(row.Detail, "; ")`. A worker's preparation failure
reaches a `KindExecuteFailed` outcome this way: wrapped as `"preparing the worker directory: "`
followed by that joined detail, rather than as the row a `prepare` call would have decided had it
run outside a worker at all.

## Applies to

`cmd/lydite/mutation.go`'s `mutationLifecycle` (`Prepare`, `StartServices`, `RunCommands`,
`Plan`'s `NotReady`) and `lifecycleRowError`/`lifecycleRow`, and any future
`internal/stages/<concern>.Lifecycle`-shaped interface a CLI adapter fills from a helper another
session's command already decided a row through.

## Example

```go
// wrong: the stage's In/Out would have to name ui.Row to carry this
func (l *mutationLifecycle) Prepare(ctx context.Context, name string, inv runner.Invocation, dir, root string, cfg config.Config, tc *toolchain.Env) (ui.Row, error) {
    row, ok := prepare(ctx, inv, dir, root, mutationLabel(name), p.c, cfg, tc, p.log)
    return row, !ok
}

// right: the decided row crosses as an opaque error, and the stage never reads it
func (l *mutationLifecycle) Prepare(ctx context.Context, name string, inv runner.Invocation, dir, root string, cfg config.Config, tc *toolchain.Env) error {
    if row, ok := prepare(ctx, inv, dir, root, mutationLabel(name), p.c, cfg, tc, p.log); !ok {
        return lifecycleRowError{row}
    }
    return nil
}
```

Reasoning: [ADR 0065](../../docs/adr/0065-a-stage-reports-its-outcome-as-data-and-the-cli-alone-decides-the-rows.md)
and [`agentic/references/architecture.md`](../references/architecture.md).
