# A stage is a function of its own `In`, and imports nothing above it

A command built on Flow is four layers, and the dependency arrow points one way: CLI
(`cmd/lydite`, presentation — flags, a command's own options struct, `ui.Report`) → flow
definitions (`internal/flows/<command>`, orchestration — which stages, in what order, under
what bindings and conditions) → stages (`internal/stages/<concern>`, the business units a flow
wires together) → domain and data (`internal/forge`, `internal/clearance`, `internal/referral`,
`internal/trust`, `internal/reviewdecision`, …). Nothing below the CLI imports `cobra`, a CLI
options struct, `internal/ui`'s report type, or `cmd/lydite`; domain packages never import
`internal/flow`, and neither do stages. A stage's signature is `func(context.Context, In) (Out,
error)`: it reads nothing but that `In`, returns nothing but its `Out`, and knows neither the
flow running it nor any other stage's state — `truststages.InitTrust` is the one stage that
reads the process environment, because establishing a run's `trust.TrustedContext` is what it
exists to do; every later stage receives that context as a field instead of reading the
environment again. A stage never picks its own `OnError` either — that is the flow definition's
call, so the same stage function means something different depending on where a flow puts it.

A stage that reaches around its own `In` — the environment, a global, the flow it is running
inside, cobra's `*cobra.Command` — is wiring that `flow.Build`'s reflection pass cannot check:
`Build` verifies that every bound field is assignable and that every `FromStage` reference names
a stage already declared, but it has no way to see a dependency a stage function reaches for on
its own. It also breaks the reason this shape exists: an API frontend answering the same webhook
a CLI command answers today has to be able to run the identical flow, and a stage that only works
inside `cmd/lydite`'s process — because it reads a flag, or a variable only that binary's
environment sets — cannot be reused by anything else that builds the same `flow.Inputs`.

## Applies to

`source/cli/internal/flow/**`, `internal/stages/**`, `internal/flows/**`, `internal/trust/**`,
`internal/forge`'s `SCMRepository`, `cmd/lydite/clearance.go`, and any future command whose logic
moves onto a Flow.

## Example

```go
// wrong: reaches around its own In for the value and for the flow itself
func Decide(ctx context.Context, in DecideIn) (DecideOut, error) {
    token := os.Getenv("GITHUB_TOKEN") // not an InitTrust stage — this one has no business reading it
    if flow.CurrentStageFailed(ctx, StageParseCommand) {
        return DecideOut{}, nil
    }
    // ...
}

// right: the value arrives as a field the flow definition bound; nothing here
// knows the flow exists
func Decide(_ context.Context, in DecideIn) (DecideOut, error) {
    if !in.CanWrite {
        return DecideOut{Answers: true}, nil
    }
    // ...
}
```

Reasoning: [`agentic/references/architecture.md`](../references/architecture.md).
