# Fold only a denominator-moving argument into a producer string

A producer compares verbatim against a baseline's, so every argument folded into it retires an
ungated change per instrument bump (ADR 0025). Fold in an argument only when it changes what the
coverage figure is a proportion of — Go's `-coverpkg` and its trailing package patterns, and by
the same reasoning Rust's `-p` and its nextest filter expressions, vitest's `--coverage.include`
and `--coverage.exclude`, jest's `--collectCoverageFrom`. Leave every other declared argument
out — a `-timeout`, a `-race` — or the producer changes on an edit that moved no figure, and a
component reads as newly measured for nothing. Render the runner's own default scope as no
suffix at all, not as its own spelling, since that is the scope every component declaring none
already measured through.

## Applies to

`Runner.Producer` and any `<lang>Scope` helper in `source/cli/internal/runner/runner.go` — today
only `goScope` for `go-test`; Rust's, JavaScript's and Python's runners take denominator-moving
arguments too — pytest-cov's `--cov=<path>` among them — but do not yet fold them in (tracked in
`coverage.md`).

## Example

```go
// wrong: every declared arg reaches the producer, so a -race edit reports the
// component newly measured though the figure it is compared against did not move
return join("go", lang, strings.Join(args, " "))

// right: only -coverpkg and the trailing package patterns move the denominator
if scope := goScope(args); scope != "" {
    return both(join("go", lang), scope)
}
return join("go", lang)
```

Reasoning: [`agentic/references/coverage.md`](../references/coverage.md) and
[ADR 0025](../../docs/adr/0025-a-baseline-records-its-producer-and-only-record-writes-it.md).
