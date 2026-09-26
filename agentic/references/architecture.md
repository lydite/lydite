# Flow architecture: `internal/flow`, `internal/stages`, `internal/flows`

> **The reference for `internal/flow`, `internal/stages/*`, `internal/flows/*`, `internal/trust`,
> `internal/forge`'s `SCMRepository`, and any `cmd/lydite` command built as a Flow.**

`clearance` is the first command built this way; `internal/flow`'s own doc comment names the
rest of the shape. A command that answers a webhook by reading a platform live, deciding
something, and writing back to the platform is the pattern this exists for — `test`, `mutation`,
`scan`, `review`, `publish` and the others stay as they are until each is migrated on its own
terms, which is a decision made command by command rather than one this file makes for them.

## Four layers, and no jumping

A command built this way is four layers, each importing only the one below it:

1. **CLI** (`cmd/lydite`) — presentation. Parses flags, builds a flow's `Params`, runs it, and
   renders `flow.Result` into a `ui.Row`. It is the only layer that imports `cobra`, `ui.Report`,
   or a command's own options struct.
2. **Flow definitions** (`internal/flows/<command>`) — orchestration. Declares which stages run,
   in what order, wired by what bindings, under what conditions and policies. A declaration and
   nothing else: it reads no environment, prints nothing, and decides no report row.
3. **Stages** (`internal/stages/<concern>`) — the business units a flow wires together. Each is a
   plain function of its own `In`, described below.
4. **Domain and data** (`internal/forge`, `internal/clearance`, `internal/referral`,
   `internal/trust`, `internal/reviewdecision`, …) — the packages that already existed, or exist
   independently of Flow, and know nothing about it.

Nothing below the CLI imports `cobra`, a CLI options struct, `internal/ui`'s report type, or
`cmd/lydite`. Domain packages never import `internal/flow`, and neither do stages: a stage's
signature is `func(context.Context, In) (Out, error)`, which compiles and tests without the flow
package in scope at all. The dependency arrow points one way — CLI → flow definitions → stages →
domain — and a change that needs to jump it (a stage that wants a `cobra.Command`, a domain
package that wants to read a `flow.Result`) is a sign the thing that needs it belongs in a
different layer, not a reason to add the import.

## A stage is a function of its own `In`

A stage reads nothing but the `In` struct the flow hands it and returns nothing but its `Out`
struct — no flow, no other stage, no shared, mutable context. `struct{}` is what a stage takes or
gives when it needs neither. This is what makes a stage callable from a plain struct literal in a
test with no fake "pipeline" to stand up, and it is why the four layers above hold: a stage that
could reach the flow around it, or a sibling stage's private state, would make "who set this,
and when" a question about *this run's* declaration order rather than about the stage's own
signature.

A stage never chooses what its own error does, either. `OnError` is the flow's call
(`flow.FailFlow`, `flow.RecordAndContinue`, `flow.BestEffort`), so the same stage function means
something different depending on where a flow puts it — the clearance flow's reply stages run
under `RecordAndContinue` because a decision that already stands should not be undone by a reply
that failed to post, while every earlier stage is `FailFlow` by default.

## The Builder: bindings, conditions, policies

`flow.New(name).Stage(name, fn)` adds a stage after every one already added — order is
declaration order, full stop. `With(field, binding)` says where one exported field of that
stage's `In` comes from:

- `FromInput(key)` — a value the caller's `flow.Inputs` supplies under that key.
- `FromStage(stage, field)` — an exported field of an *earlier* stage's `Out`. `Build` refuses a
  reference to a stage not yet declared, so a flow's own text is always read top-to-bottom in the
  order its data actually flows.
- `Literal(v)` — the same value on every run.

`Build` checks the whole declaration by reflection: every exported `In` field bound exactly once,
every binding's source assignable to the field it feeds, every condition bound to a `bool`, every
`OnError` a real `Policy`. A flow that builds is one whose wiring is correct — `Run` can only fail
for a reason its own doc comment enumerates (a missing input, a stage's own error, a stage
reading an unavailable output), never for a typo in a field name or a binding that came from
nowhere. That guarantee is what a hand-rolled, reflection-checked builder buys over stringing
stages together by hand: the check runs once, at `Build`, rather than being re-derived by a
reader for every call site.

`When(binding)`/`Unless(binding)` add a condition, each bound to a `bool`; every condition on a
stage must hold for it to run, and they are evaluated in declaration order, stopping at the
first that does not — the rest, including one that would read an unavailable stage's output, are
never evaluated at all.

### The ordering subtlety

A condition that reads another stage's own output must be declared *after* the condition that
already implies that stage ran — never before it, and never alone. The clearance flow's stages
past `parse-command` all declare, in this order:

```go
Stage(StageDecide, clearancestages.Decide).
    When(addressed).
    With("Repository", repository).
    // …
Stage(StageFingerprint, clearancestages.Fingerprint).
    When(addressed).When(clears).
    // …
```

`clears` is `FromStage(StageDecide, "Clears")`, and `decide` itself only runs `When(addressed)`.
Declaring `When(addressed)` first on `fingerprint` too is what makes reading `clears` safe:
`holds` stops at the first condition that does not hold, so a comment nothing addressed never
reaches the `clears` condition at all. Reversing the two — `When(clears).When(addressed)` — would
evaluate `clears` first on every run, including the ones where `decide` never ran, and reading a
skipped stage's output is not "skip this stage too": it is `*StageError` wrapping
`flow.ErrUnavailable`, which stops the whole run. A condition on a stage's own output is only
ever safe once every condition guaranteeing that stage ran already precedes it in the same list.

## Why execution is sequential

`flow.Run` runs every stage strictly in declaration order — one at a time, no dependency graph
inferred from the bindings, and none is to be grown here. This is deliberate for two reasons
already established elsewhere in this codebase's own language:

- **The Scheduler's own restraint is not an oversight to fix here.** `internal/scheduler` runs a
  **Shard**'s independent components concurrently, under no ordering but a physical lock on a
  shared port — "there is no execution graph, deliberately," because two components have no
  declared relationship to serialise on. A flow's stages are the opposite case: a later stage's
  `In` is routinely bound to an earlier stage's `Out`, so the data itself has an order, and
  `Build`'s own rule — `FromStage` may only name a stage already declared — is what keeps that
  order identical to the text a reader reads top to bottom. Inferring an execution order from the
  bindings, the way a real pipeline library would, would let a flow's declared order and its
  actual run order disagree; refusing to build one is what keeps them the same thing.
- **A Gate that could not run must never render as one that passed**, and neither may a stage
  that never ran render as one that returned a zero value. `flow.Output[T]` and every internal
  read of a stage's output enforce this at the engine level: a skipped or failed stage's output
  is `ErrUnavailable`, not a zero `Out{}` a later stage or the CLI could mistake for a real
  answer. Running stages one at a time, with no attempt to run an unrelated stage "in the
  meantime," is what keeps this check total — a concurrent engine would need the same guard at
  every point two stages' lifetimes could overlap, rather than once, in `Run`'s own loop.

## The payload only points; trust and SCM come first

`clearanceflow.New` declares `init-trust` and `init-scm` before anything else, and
`init-trust` is the only stage in the flow that reads the process environment (every later stage
receives the `trust.TrustedContext` it built). The comment a command arrived on is then resolved
*live*, from its id alone (`forge.ReadCommentRef` reads only the id and the payload's claimed
repository) — never trusted for its body, author or pull request number, all of which a webhook
payload carries but which can be stale, edited, or simply wrong by the time a job gets to them.
`LoadComment` additionally checks the payload's claimed repository against the trusted one before
fetching anything, and refuses a mismatch outright rather than fetching against the trusted
repository instead — a payload is evidence of *which* comment to read, never of what it says.
This is why trust and the repository are resolved before the comment: reading the comment needs
both, and refusing a mismatched payload before any request is made is cheaper and safer than
fetching first and refusing to act on the result.

## Package layout

- **`internal/flow`** — the engine: `Builder`, `Binding`, `Policy`, `Flow`, `Result`. Knows
  nothing about any command.
- **`internal/stages/<concern>`** — one package per concern, holding the stage functions and
  their `In`/`Out` types. Two kinds:
  - **Generic** — `truststages` (`internal/stages/trust`) and `scmstages`
    (`internal/stages/scm`) hold stages usable by any flow that needs a `TrustedContext` or reads
    and writes the hosting platform through `forge.SCMRepository`.
  - **Domain** — `clearancestages` (`internal/stages/clearance`) holds stages specific to
    answering a clearance command: parsing it, deciding it, fingerprinting it, composing its
    reply.
- **`internal/flows/<command>`** — one package per command, holding that command's `New() (*flow.Flow, error)`, its `Params`, and the `Input`/`Stage` name constants a caller (today, only
  the CLI) reads a `flow.Result` back through.

Every stage package is named apart from the domain package it wraps — `truststages` beside
`internal/trust`, `scmstages` beside `internal/forge`, `clearancestages` beside
`internal/clearance` — so that a flow definition, which routinely needs both (a stage's `In`
typed in the domain package's own terms, and the stage package that produces it), can import
both without renaming either on the way in. The naming is stated once, in each stage package's
own doc comment, rather than left for a reader to infer from the fact that two packages share a
directory prefix.

## Why hand-rolled, over a pipeline library

`internal/flow` is under 700 lines across its two files and depends on nothing beyond the
standard library's `context`, `errors`, `reflect` and `io`. Reaching for a general pipeline
library instead — `google/go-pipeline` and its relatives share the shape of a DAG of named
steps — would trade that for a dependency lydite does not control the release cadence of,
to get a shape none of them is built around: `internal/forge`'s own package doc states the
argument this reuses — "lydite's dependency set is part of its argument," because every tool it
runs is pinned to a manifest something can age out, and a small, auditable file lydite owns is
cheaper to keep correct than a general one it doesn't. Three properties specific to this
codebase are not things an off-the-shelf pipeline library is designed to give:

- **Bindings typed and validated at `Build`.** A generic step-graph library typically wires steps
  by name, with a step's own input read out of a shared, untyped context (a `map[string]any`, or
  a context value) — which pushes the same checks `flow.Build` runs once onto every reader of
  that context, forever, with no compiler or reflection pass to catch a typo before `Run`.
- **No shared context.** A stage seeing nothing but its own `In` is what keeps a stage
  unit-testable in isolation and un-reachable from a sibling stage's own state; a shared context
  is precisely the shape that makes "which stage set this, and when" unanswerable from a stage's
  signature alone.
- **`flow.Output[T]`'s `ErrUnavailable`.** A general library's "did this step run" is usually a
  status enum a caller may or may not check; wrapping a skipped or failed stage's output in an
  error that a type-parametrised read cannot silently coerce into a zero value is what makes "a
  gate that could not run never renders as one that passed" a property of the engine, not a
  discipline every caller has to remember to keep.

None of the three is a large amount of code once decided on, which is the rest of the argument:
a dependency is worth taking only when what it buys costs more to build than to keep pinned and
updated, and here the reverse held.
