# A flow is a hand-rolled engine of typed bindings, not a pipeline library or a shared context

`clearance` is the first `cmd/lydite` command answering a webhook by reading the platform live,
deciding something, and writing back to it — the shape `internal/flow` exists for. Before writing
its stages, three ways of wiring them together were available, and this records why the one built
is `internal/flow`: a stage is a plain `func(context.Context, In) (Out, error)`; a `Builder`
(`flow.New(name).Stage(name, fn)`) wires stages by declaration order and binds each exported field
of a stage's `In` with `With(field, binding)` to `FromInput(key)`, `FromStage(stage, field)` or
`Literal(v)`; `When`/`Unless` gate a stage on a bound `bool`; `OnError` sets a stage's `Policy`
(`FailFlow`, `RecordAndContinue`, `BestEffort`); and `Build` checks the whole declaration by
reflection — every field bound exactly once, every binding's source assignable to the field it
feeds, every condition a `bool`, every policy real — before `Run` ever executes a stage.

## Rejected: a general pipeline or DAG library

`google/go-pipeline` and its relatives wire named steps into a dependency graph and read a step's
input out of a shared, untyped context — a `map[string]any` or a context value a step reads a key
out of at its own risk. Two things rule this out beyond taste.

First, `internal/forge`'s own package doc states the argument this reuses: "lydite's dependency
set is part of its argument" — every tool lydite runs is pinned to a manifest something can age
out, and a client or engine lydite owns outright is cheaper to keep correct than a general one
it doesn't control the release cadence of. `internal/flow` is under 700 lines across two files and
imports nothing beyond the standard library's `context`, `errors`, `reflect` and `io`.

Second, and more specific to this codebase: a pipeline library's whole reason to exist is
scheduling a dependency graph, and lydite has already settled, in a different package, that this
is not a shape it wants. CONTEXT.md's own **Scheduler** entry states the same restraint for
`internal/scheduler`, which runs a Shard's independent components under no ordering but a
physical lock on a shared port — "there is no execution graph, deliberately," because two
components with no declared relationship have nothing to serialise on. A flow's stages are the
opposite case: a later stage's `In` is routinely bound to an earlier stage's `Out`, so the data
itself already has an order, and that order is exactly declaration order — `Build` refuses a
`FromStage` naming a stage not yet declared, so a flow's own text is always read top to bottom in
the order its data flows. Inferring an execution order from the bindings, the way a real pipeline
library would, would let a flow's declared order and its actual run order disagree; refusing to
build one is what keeps them the same thing. No execution graph is wanted here for the same
reason none is wanted in the Scheduler, even though the two packages are answering different
questions.

## Rejected: a shared typed context stages read from and write to

The second shape considered was a `Context` or `View` value threaded through every stage, keyed
by field or by name, that a stage reads its inputs from and writes its outputs into. It reads as
less ceremony than a `Builder` — no bindings to declare, just read the key you want — and that is
exactly its defect. A stage reading a shared context depends on implicit state: whether an earlier
stage wrote the key it wants is a fact about *this run's own declaration order*, not about the
stage's own signature, so nothing catches a flow that reads a key nothing upstream of it ever
wrote until the stage runs and gets a zero value it cannot tell apart from a real one. The wiring
cannot be checked before `Run`, because there is no wiring to check — only a set of keys a stage
happens to read and a set another happens to write, matched by nothing more than both authors
having typed the same string. And a stage that reads the shared context is a stage that knows the
flow around it, which is the one thing a stage must not do: `internal/flow`'s own contract is that
a stage reads nothing but its own `In` and returns nothing but its own `Out`, so that it is
callable from a plain struct literal in a test with no fake pipeline to stand up.

## Rejected: stages defined inside `cmd/lydite`

The third alternative was not a different engine but a different place to put the units it runs:
writing `clearance`'s stages directly inside `cmd/lydite/clearance.go`, wired by ordinary Go
function calls, with no `internal/flow` at all. This is the simplest thing that could work for one
command, and it is rejected because it is not a decision about one command — it ties every stage
to the CLI layer, so nothing outside a `cobra` invocation could ever run the same decision. The
four layers `internal/flow` is built to keep — CLI, flow definitions, stages, domain, each
importing only the one below it — exist because a stage that could import `cobra` or
`internal/ui`'s report type is a stage a future API frontend could not call, and lydite's own
`internal/flow` doc comment names the rest of the migration as stages, not as CLI code grown
another layer.

## Consequences

- Every command `internal/flow` is meant for — `test`, `mutation`, `scan`, `review`, `publish` and
  the rest — migrates onto this scaffold eventually, each on its own terms and each its own piece
  of work; this decision states the scaffold, not the schedule.
- A condition reading another stage's own output must be declared after the condition that already
  guarantees that stage ran, never before it and never alone — `flow.Run`'s `holds` stops at the
  first condition that does not hold, so a condition placed too early on a stage's own output turns
  a should-be-skipped run into a `*StageError` wrapping `flow.ErrUnavailable`. The clearance flow's
  own `decide`/`fingerprint` pair is the example: `fingerprint` declares `When(addressed).
  When(clears)`, in that order, precisely because `decide` itself only runs `When(addressed)`.
- `init-trust` is the one stage in a flow built this way that reads the process environment; every
  later stage receives the value it produced rather than reading again. Concentrating the one
  impure read at the top of the flow is deliberate — see
  [ADR 0061](0061-trust-and-the-repository-come-first-and-a-webhook-payload-only-points.md).

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md) for the
layering this decision produces and the full account of why hand-rolled beats a pipeline library.
