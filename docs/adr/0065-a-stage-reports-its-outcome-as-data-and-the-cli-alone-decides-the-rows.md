# A stage reports its outcome as data, and the CLI alone decides the rows

`mutationstages.RunMutants` is the one stage that runs every selected component's mutants, on
the existing `scheduler.Run`, `mutation.Slots` and `mutation.Execute` — the per-component work
stays inside it rather than moving into a stage per phase. What it hands back is a
`ComponentOutcome` per component, carrying an `OutcomeKind` — one per distinct row a component
can earn — and the facts that row needs, and `RunMutants` builds no `ui.Row` and no `ui.Report`.
`mutationflow.New` wires it as
the last of six stages, `mutationflow.NewRecord` runs `RecordMutants` alone on the outcomes
whose verdict the CLI has already decided stands, and `cmd/lydite/mutation.go`'s
`addMutationRows` is the one place a `ComponentOutcome` becomes a row.

## Rejected: a stage per phase

`RunMutants` could have split into a stage that plans, a stage that provisions each worker, a
stage that schedules, and a stage that records — one stage per phase of the per-component work
`scheduler.Run`, `mutation.Slots` and `mutation.Execute` do together. It is rejected because the phases are not
independent data a flow's bindings could wire: a worker is opened once per concurrency slot and
reused across every mutant that slot runs, `scheduler.Run` interleaves components under one
port lock, and a mutant that survives its own package's tests is run again against the wider
closure only then. None of that is a later stage reading an earlier stage's `Out`; it is one
scheduler dispatching one component's whole lifecycle, and splitting it across stage boundaries
would need the engine to run stages concurrently and let them share state across mutants of one
component, both of which the flow engine is frozen against (ADR 0059). Per-component work stays
where the scheduler already does it, and the flow runs its one stage once.

## Rejected: rows, or methods returning `ui.Row`, in a stage

A stage could return `ui.Row` directly, or an interface method like `Lifecycle.Prepare` could
answer `(ui.Row, bool)` instead of an opaque `error`. Both are rejected for the same reason: a
stage that decides a row is a stage that knows about `internal/ui`'s report type, which the
architecture confines to the CLI layer, and a stage that reads `ui.Document` or `ui.Comment` as
*input* data (as `internal/stages/clearance/reply.go`'s reply stage already does) is not the
same thing as a stage that *produces* one. `RunMutants` answers with an `OutcomeKind` — one per
distinct row `mutateComponent`/`prepareMutation` could return, so two call sites share a kind
only when their rows are byte-identical — and the facts that kind's row needs (an error, a
report path, a component name), never the row's wording. Wording a row is `mutationLabel`,
`kindRow`, and the rest of `cmd/lydite/mutation.go`'s job, done once, so a row's text changes in
one place regardless of which stage's outcome produced it.

## Rejected: a stage-owned `Failure` struct mirroring the CLI's rows

A middle ground considered was a `mutationstages.Failure` type — a kind, a message, a detail
slice — that the stage builds and the CLI renders into a row by copying its fields across. It
reads as keeping the row out of the stage while still giving the CLI something typed to work
from, and it is rejected because it is a row under a different name: every field a `Failure`
would need to carry is a field `ui.Row` already has, so the type would drift the moment
someone added a row-only concern (a status colour, a `--json` tag) to one but not the other.
`OutcomeKind` plus the outcome's own facts is the leaner shape, because the CLI already knows,
for each kind, exactly which row it becomes — that mapping is `kindRow`'s whole body, and it
needs no intermediate struct shaped like its own output to get there.

## Rejected: parallel stages, and an engine change to let a stage outrun cancellation

Two further changes were floated and rejected without reworking `internal/flow` for either.
Splitting `RunMutants`'s components across several stages running concurrently, to parallelise
mutant execution at the flow level rather than inside the scheduler, was rejected because
`flow.Run` runs every stage strictly in declaration order (see `architecture.md`'s "Why
execution is sequential") — mutant concurrency is unchanged, and stays where
`mutation.Slots`/`mutation.Execute` already bound it. Letting a stage ignore `ctx.Err()` so
that a run interrupted mid-flow could still finish its current stage was rejected too: `flow.Run`
checks the context before every stage unconditionally, and carving out an exception for one
flow would make "a flow stops before its next stage once cancelled" no longer a property every
flow gets from the engine.

## Consequences

- A lifecycle helper the CLI already owns — `prepare`, `runCommands`, `startServices` — has
  already decided a row before `mutationstages.Lifecycle`'s interface is ever called, because
  their `(ui.Row, bool)` shape is pinned by other sessions' own tests. The CLI's adapter wraps
  that row in a `lifecycleRowError` and returns it as a plain `error`; the stage treats it as
  opaque and hands it back unread, and the CLI recovers it with `errors.As` only for a
  `KindBlocked` outcome or a teardown. `lifecycleRowError.Error()` is the row's detail joined by
  `"; "`; a worker's preparation failure never reaches the report through a decided row at
  all — only through the executor's own error, `preparing the worker directory: ` followed by
  that joined detail, which `KindExecuteFailed` renders as an unmeasured row carrying the
  executor's text verbatim, with no `errors.As` unwrapping it. See
  [the rule this decision produced](../../agentic/rules/a-row-a-shared-helper-already-decided-crosses-into-a-stage-as-an-opaque-error.md).
- `Scheduled` and `Interrupted` are facts `RunMutants` states about what it did — which
  components it handed to the scheduler, and whether cancellation cut the run short — not a
  verdict on whether that outcome counts. Deciding which interrupted verdicts to withdraw stays
  with the CLI's `withdrawInterrupted`, because withdrawal depends on the row a component earned
  (a survivor withdraws; `KindMutationOff` does not), and a stage that decided withdrawal would
  need to know the row it was never given.
- `mutationflow.NewRecord` holds `RecordMutants` alone, in a flow of its own rather than as
  `New`'s seventh stage, because `flow.Run` checks `ctx.Err()` before every stage: a flow that
  ran `RecordMutants` as part of `New` would skip it on the same interrupt that cut `RunMutants`
  short, and a run cut short still has to record the components that finished. The CLI runs
  `NewRecord` on `context.WithoutCancel(ctx)` whenever `RunMutants`' output is available, exactly
  as `recordMutants` does. The accepted residual this leaves: an interrupt landing between two
  stages of `New()` itself — after `ScopeChange` and before `RunMutants`, say — returns the bare
  `context canceled` error rather than a "not run" row for every selected component, and records
  nothing for that run. Splitting `New()` further to isolate that gap was rejected as not worth a
  seventh stage boundary for a window one `ctx.Err()` check already closes almost everywhere
  else.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md)'s "Mutation
flows" section for where these stages sit among the four layers, and
[ADR 0066](0066-shard-documents-are-read-by-one-generic-stage-both-folds-share.md) for the same
reasoning applied to what a fold reads before it decides anything.
