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

See [ADR 0061](../../docs/adr/0061-trust-and-the-repository-come-first-and-a-webhook-payload-only-points.md)
for the decision and why reading the payload's body or its repository claim directly was rejected.

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

## The scan flow: components walked inside a stage, not fanned out by the engine

`scan` is the second command built on Flow, and the first whose whole job is to visit every
declared component rather than one pull request. `internal/flow.Run` still executes one declared
list of stages, each called once — it has no notion of running a stage N times, once per
component an earlier stage produced — so `scanflow.New` declares eleven stages, and a stage that
must act on every component (`plan-components`, `run-checks`, `gate-licences`, and
`provision-toolchains`, `warn-unscanned`) walks `[]component.Component` or the `[]Planned` an
earlier stage produced in its own body, in declaration order:

`load-config` → `load-components` → `resolve-diff-base` → `read-changed-lines` →
`provision-toolchains` → `warn-unscanned` → `plan-components` → `run-checks` → `gate-licences` →
`semgrep` → `secrets`.

`run-checks` and `gate-licences` each return a slice the same length, and at the same index, as
the `[]Planned` `plan-components` produced — a plan entry that is not `Scan` reads back as the
zero value in each, so one component's checks or licence verdict can never be read at another's
index. See [ADR 0067](../../docs/adr/0067-scan-runs-as-eleven-single-responsibility-stages-walking-components-in-their-own-body.md)
for why the walk sits inside a stage rather than being one stage per component or per language,
and why `gate-licences` is its own stage rather than folded into `run-checks`.

**`semgrep` and `secrets` condition on `load-config`'s own output with no preceding guard,
unlike the ordering subtlety above.** Both are declared `When(flow.FromStage(StageLoadConfig,
"SemgrepEnabled"))` and `When(...,"SecretsEnabled")` with nothing declared ahead of that
condition — safe here for a reason the clearance flow's `decide`/`fingerprint` pair does not share:
`load-config` is declared first, under no condition of its own and the default `FailFlow`, so by
the time either later condition is evaluated `load-config` has already either produced the output
being read or its own `FailFlow` error has already stopped the run. The ordering subtlety applies
to a condition reading a stage that itself runs conditionally; `load-config` never does, so there
is no earlier condition to declare first.

**Three inputs are declared as interfaces in `scanstages` and adapted by the CLI**, so no stage
below it imports `cobra` or reads the process environment: `Toolchains.Ensure` provisions each
scan unit's language toolchain (`cmd/lydite`'s `scanToolchains` wraps the command's own
toolchain provisioning); `Environment.Compose`/`.Declared` composes a component's checks
environment and reports what its declaration alone contributed (`scanEnvironment` wraps the
command's `childEnv`/`splitPath`); `Diagnostics io.Writer` is where a stage writes its own
warnings as they arise rather than returning them — see
[ADR 0068](../../docs/adr/0068-a-stages-diagnostics-are-written-as-they-arise-not-returned.md) —
and the CLI supplies `cmd.ErrOrStderr()`.

**The CLI renders by walking the same plan the stages walked.** `recordComponents`
(`cmd/lydite/scan.go`) reads `plan-components`'s `[]Planned` once and, per entry in declaration
order, reads `run-checks`'s and `gate-licences`'s output at that same index — a non-`Scan` entry's
unscanned, off-by-default or duplicate row; a `Scan` entry's check rows, findings and licence
verdict, in that order. `semgrep` and `secrets` are read afterwards, each only when
`!r.Skipped(stage)`: a gate a run's own configuration switched off contributes nothing, because a
zero result in its place would render as a check that ran and found nothing — the same rule
[the gate-that-could-not-run rule](../../agentic/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md)
states for a gate that could not run at all.

## Package layout

- **`internal/flow`** — the engine: `Builder`, `Binding`, `Policy`, `Flow`, `Result`. Knows
  nothing about any command.
- **`internal/stages/<concern>`** — one package per concern, holding the stage functions and
  their `In`/`Out` types. Two kinds:
  - **Generic** — `truststages` (`internal/stages/trust`) and `scmstages`
    (`internal/stages/scm`) hold stages usable by any flow that needs a `TrustedContext` or reads
    and writes the hosting platform through `forge.SCMRepository` — see
    [ADR 0060](../../docs/adr/0060-stages-read-and-write-the-platform-through-an-scmrepository-interface-ahead-of-a-second-vendor.md)
    for why that is an interface, and built from a `TrustedContext` alone, ahead of a second
    vendor.
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

## Test

`test` is the second command built this way, migrated by #270 onto seven stages in
`internal/stages/test` (package `teststages`), wired in order by `internal/flows/test` (package
`testflow`): `Declaration`, `Toolchains`, `SelectAffected`, `PrepareFlakyGate`, `Run`,
`FlakyGate`, `Coverage`. None is conditioned — each already handles having nothing to do
internally (selection when nothing was asked for, `Run` when nothing was selected, `Coverage`
when nothing is declared or instrumented), because a flow-level condition would leave that
stage's outputs unavailable to every stage declared after it.

Two packages sit underneath the stages, split by what they own rather than by report section:

- **`internal/test/measure`** turns one run of a component's instrumented suite into the
  coverage and CRAP figures every gate reads, and renders the rows those gates report. It
  executes no suite and writes no file — it is a function of the measurements and baselines it
  is handed — because `lydite test`'s own run and `lydite test merge` folding a matrix of
  shards' documents both need the identical answer, and two copies that agreed today would come
  apart the day one learned about a case the other had not.
- **`internal/test/run`** is the engine: planning each selected component, scheduling them under
  the port and directory locks they declare, preparing, starting services, running and tearing
  down each one, and the flaky gate that reruns the tests a change introduced from inside that
  same run. It owns no log — every function that writes a component's output takes an
  `Output{W, Rel}` — so the CLI's own log-opening decisions (a file, a mirror to the terminal, or
  nothing) stay the CLI's.

### `componentPlan`, `componentLog`, `measurementsDoc` and `componentMeasurement` stay in `cmd/lydite`

These four types are test/coverage logic by every other measure, and were left out of the move
anyway: `cmd/lydite/mutants.go`, `merge.go` and `record.go` — owned by other sessions in the same
milestone, and out of reach of this one — reach into their *unexported fields and methods*
directly (`p.c`, `p.log`, `folded.snapshot()`, `e.asMeasurement(c)`), and no alias or rename
survives a type changing package: a field selector or a method call on a value of that type
breaks the moment the type is no longer the one declared where the caller's own compiler unit
sees it. This is the decision the rest of the migration is arranged around, and it generalises: a
future stage extraction should check, symbol by symbol, whether the calling file reaches a
*field or method* on a type (the type has to stay where it is) or merely *calls a function*
(safe to move behind a same-signature wrapper the CLI keeps).

### `measurements.json` stays a `cmd/lydite` document

`cmd/lydite` still builds and writes `measurements.json` through the pre-existing
`measurementsFrom`/`writeMeasurements`, fed by a `teststages.Candidate` the `Coverage` stage
returns — not a document type duplicated into `internal/test/measure` or anywhere else. The
symbol-table analysis that preceded the move found duplicating the schema into the new package
was also possible, guarded by a golden-fixture test asserting the two never drift — but writing
the one document from the one place that already writes it needs no duplicate, and no drift
guard, at all.

### The flaky gate is fused inside `Run`, split only at the report boundary ([ADR 0062](../../docs/adr/0062-flaky-gate-is-its-own-stage-kept-in-lockstep-with-run-components.md))

`Run` and `FlakyGate` are separate stages, matching the one-stage-per-report-section shape every
other stage in this flow holds to — but the flaky re-examination itself still happens fused
inside each component's own run in `internal/test/run`, exactly as it did before the migration:
`Run` hands the same `*testrun.FlakyGate` pointer it ran with back unchanged as its own output,
and `FlakyGate` calls that pointer's `Report` method afterward to produce its rows. A future
change that reorders these two stages, or changes what either writes to a shared output stream,
must re-verify the invariant this split depends on rather than assume it holds because the tests
pass: the two stages' rows, findings and measurements, concatenated, must equal what one fused
pass over the same fixture would have produced. `internal/stages/test/flaky_test.go` is that
check today, run over the fused sequence and the two-stage sequence side by side.

### Two behavioral divergences from the pre-migration engine

- **`Coverage`'s output findings do not reach the report.** `CoverageOut.Findings` carries the
  patch findings and gated-CRAP findings `gatedRows` produces, and `addCoverage` in `cmd/lydite`
  does not call `rep.AddFindings` on them — the same gap the pre-migration `addCoverageRows` had.
  The drop predates this migration; #285 tracks it as a separate change, since fixing it here
  would add findings to some reports that #270's own byte-diff verification depends on being
  identical to the pre-migration binary's.
- **A genuine mid-run interrupt (`SIGINT`/`SIGTERM`, a CI job timeout) omits the flaky-gate and
  coverage sections from the report**, rather than rendering them the way the fused
  pre-migration engine effectively did — every one of its own checks was already a no-op against
  an already-cancelled context, so it rendered sections whose content said nothing. The run
  still fails correctly: the schedule row already reports the run as cut short, and nothing
  downstream of that renders as passing. Closing this fully would mean either duplicating the
  flow's own wiring inside the CLI so it can render partial output for a stage that never ran, or
  changing `internal/flow`'s cancellation semantics so a stage that never started can still
  contribute a row — both a larger change than this migration's own scope.

### Open seams for a future `record.go`/`reports.go` consolidation

The symbol-table analysis found no category-3 duplication among symbols this migration owns, but
did find small private copies needed because the logic being moved calls a helper declared in a
file this migration does not own:

- `unmeasurableByDeclaration` in `internal/test/measure` mirrors `record.go`'s function of the
  same name, guarded by a test asserting the two answer identically across every runner,
  argument and command shape a component can declare.
- `ignoreReports` in `internal/test/run` mirrors `reports.go`'s, guarded by a test asserting the
  two are byte-identical.
- `shortSHA` has small private copies in `internal/test/run` and `internal/stages/clearance`
  alike, with no guard — the precedent the clearance pilot already set, followed rather than
  revisited here.

A future consolidation of `record.go` and `reports.go` into a shared package would retire all
three; until then, each is a seam a change to either side's copy has to notice and carry across.

## Why hand-rolled, over a pipeline library

See [ADR 0059](../../docs/adr/0059-a-flow-is-a-hand-rolled-engine-of-typed-bindings-not-a-pipeline-library-or-a-shared-context.md)
for the decision and its rejected alternatives — a pipeline library, a shared typed context, and
stages defined inside `cmd/lydite`.

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
