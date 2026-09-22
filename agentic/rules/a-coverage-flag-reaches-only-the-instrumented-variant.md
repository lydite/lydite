# A declared coverage flag reaches only the instrumented variant

`go test -coverpkg=X` instruments with no `-cover` of its own, so a component's declared
`args:` cannot be handed to Plain or BuildOnly unfiltered — that silently turns on
instrumentation nothing reads, and Plain runs once per mutant during mutation testing, where
the cost is paid thousands of times. Any new runner variant that builds its argv from a
component's declared args must route it through `dropCoverage` (via `goTestUninstrumented` or
the language-appropriate equivalent) unless that variant is itself the one producing coverage.

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
