# Write a stage's diagnostic to the injected writer as it arises, only return it in `Out` when nothing else interleaves

The clearance pilot's stages return a diagnostic (`Warnings []string`) in their `Out`, for the
CLI to print once the flow has finished — safe there because nothing else on clearance's path
streams anything of its own to interleave with. The scan flow's stages cannot use that shape:
every scanner they run streams its own output live through `executil`, and a warning collected
into `Out` for after-the-fact printing always lands after every check's own streamed output,
regardless of which component either one was actually about. A stage whose diagnostic needs to
sit next to the tool output it is about writes it, at the moment it arises, to an injected
`Diagnostics io.Writer` instead — the shape `scanstages`' `warn-unscanned`, `run-checks` and
`semgrep` all use.

Neither shape is the flow architecture's default; a flow's own stages decide which fits, based on
whether anything else they do streams output of its own. A stage whose `Out` carries a
diagnostic-shaped field going forward is one that decided the pilot's shape fits it — not one
that forgot to wire a `Diagnostics` writer through.

## Applies to

Any new stage, in `scanstages` or a future flow's stage package, that has something to say beyond
its `Out` — a warning, a note, a skip reason.

## Example

```go
// wrong: a stage on a flow whose other stages stream their own output collects
// its warning into Out, so it always prints after every check's streamed output
type Out struct {
    Warnings []string
}

// right: written to the shared stream at the moment it arises, next to the
// tool output it is about
func RunChecks(ctx context.Context, in In) (Out, error) {
    if len(names) > 0 {
        fmt.Fprintf(in.Diagnostics, "warning: %s's checks are composed with the environment %s declares: %s\n",
            c.Name, component.FileName, strings.Join(names, ", "))
    }
    // ...
}
```

Reasoning: [ADR 0068](../../docs/adr/0068-a-stages-diagnostics-are-written-as-they-arise-not-returned.md)
and [`agentic/references/architecture.md`](../references/architecture.md#the-scan-flow-components-walked-inside-a-stage-not-fanned-out-by-the-engine).
