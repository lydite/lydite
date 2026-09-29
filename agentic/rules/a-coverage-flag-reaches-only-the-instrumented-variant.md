---
description: "A component's declared coverage flags reach only the instrumented runner variant, never Plain or BuildOnly."
---

# A declared coverage flag reaches only the instrumented variant

`go test -coverpkg=X` instruments with no `-cover` of its own, so a component's declared
`args:` cannot be handed to Plain or BuildOnly unfiltered — that silently turns on
instrumentation nothing reads, and Plain runs once per mutant during mutation testing, where
the cost is paid thousands of times. Any new runner variant that builds its argv from a
component's declared args must route it through `dropFlags` (via `goTestUninstrumented` or
the language-appropriate equivalent) unless that variant is itself the one producing coverage.

BuildOnly is stricter still: for Go it runs `go build`, which rejects every flag that only means
something to a test binary. `goBuildArgs` therefore also drops `-timeout`, `-run`, `-count`, the
profiling, benchmark and fuzz flags and the test binary's own controls (`goBuildRejected`), so a
declared `-timeout 30m` reaches Plain and Instrumented but never BuildOnly. A carried-across flag
fails the build with a usage error, which reads as every mutant being unviable.

## Applies to

`buildGoTest` and any sibling `build<Lang>Test` in `source/cli/internal/runner/runner.go` that
derives Plain's or BuildOnly's argv from a component's declared `args:`.

## Example

```go
// wrong: Plain and BuildOnly inherit every declared flag, coverage included
case Plain:
    return Invocation{Name: "go", Args: append([]string{"test"}, pkgs...)}, true

// right: coverage flags and their separately-passed values are stripped first
case Plain:
    return Invocation{Name: "go", Args: append([]string{"test"}, goTestUninstrumented(pkgs)...)}, true
```

Reasoning: [`agentic/references/components.md`](../references/components.md) and
[`agentic/references/coverage.md`](../references/coverage.md).
