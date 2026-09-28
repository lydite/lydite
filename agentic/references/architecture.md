# Flow architecture: `internal/flow`, `internal/stages`, `internal/flows`

> **The reference for `internal/flow`, `internal/stages/*`, `internal/flows/*`, `internal/trust`,
> `internal/forge`'s `SCMRepository`, and any `cmd/lydite` command built as a Flow.**

`clearance` is the first command built this way; `internal/flow`'s own doc comment names the
rest of the shape. Business logic — what lydite does to, or concludes about, a repository:
reading its state, deciding a verdict, writing a result back to it or to disk — runs on this
scaffold; the CLI's own concerns, the tool managing itself (`version`, `update`, the update
nudge), do not. See [ADR 0076](../../docs/adr/0076-business-logic-runs-as-a-flow-and-the-cli-keeps-its-own-concerns.md)
for the principle and the verdict for every command.

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

`review` reads its own payload pointer this way too — the pull request it is about, not the
comment it replies to — but declares trust and SCM late rather than first; see
[ADR 0071](../../docs/adr/0071-a-review-reads-its-pull-request-from-the-payload-and-posts-to-the-revision-it-measured.md)
and "The review flow" below for why the two commands differ.

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

## `record`: one condition, a deferred write, and a boundary type over another command's document

`lydite test record` is built this way too. `recordstages`
(`internal/stages/record`) holds `LoadDeclaration`, `ReadReports`, `FoldMeasurements`, `BindTree`,
`CountFindings`, `BindMutants`, `ComposeLedgerInputs` and `DecideBaseline`; `ledgerstages`
(`internal/stages/ledger`) holds `ComposeRecords` and `WriteState`; `recordflow`
(`internal/flows/record`) wires the two packages' stages together in that order;
`cmd/lydite/record.go` builds `recordflow.Params`, runs the flow, and renders every row from
`flow.Result`.

`load-declaration`, `read-reports`, `fold-measurements` and `bind-tree` run unconditionally, each
`FailFlow` by default: a declaration that cannot be read, a set of report directories holding no
measurements document, or a checkout whose tree cannot be resolved leaves nothing a recording
could be filed against. `bind-tree` is the exception among the four — a mismatch between the tree
that is checked out and the tree the measurements describe is its *answer*, `Bound == false`,
never its error. Every stage after it — `count-findings`, `bind-mutants`, `compose-ledger-inputs`,
`compose-records`, `decide-baseline`, `write-state` — declares exactly one condition, `.When(bound)`
reading `bind-tree`'s own `Bound` field, and no stage declares any other. [The ordering
subtlety](#the-ordering-subtlety) does not apply to this flow: that rule matters
only when a condition reads a stage that itself ran conditionally, because a skipped stage's
output is `flow.ErrUnavailable` and reading it unguarded stops the run. `bind-tree` has no
condition of its own and fails the run outright on its own error, so every stage reaching for its
output is guaranteed to find it there — there is no guard-precedence chain to build, because there
is only the one guarded stage upstream of every reader.

### `recordstages` decides what was measured; `ledgerstages` decides how history is composed and landed

`compose-ledger-inputs` (`recordstages.ComposeLedgerInputs`) is where the two packages' concerns
meet. It turns what the recording folded — the measurements, the per-component and root-scoped
finding counts, the mutant counts, and the scan's own crashed buckets — into the ledger's own
vocabulary: a `map[string]ledger.Component` per component's scalars, the root-scoped finding
counts, and the set of finding buckets this recording measured. Everything downstream of it,
`compose-records` (`ledgerstages.ComposeRecords`) and `write-state` (`ledgerstages.WriteState`),
takes those three values as plain data and knows nothing about a report document, a `finding.Crash`,
or which command produced either — `ledgerstages`' own package doc states this apart from
`internal/ledger`, the history's format and reader, the way `truststages`' and `scmstages`' doc
comments name themselves apart from the domain packages they wrap. Neither stage package imports
the other; `recordflow` is what binds `compose-ledger-inputs`' `Out` to `compose-records`' `In`, the
same `flow.FromStage` reference every other value crosses a stage boundary by in this flow.

`compose-records` answers with a `gitstate.Records` function rather than the records themselves,
carried across the stage boundary as an ordinary `Out` field (`ComposeRecordsOut.Records`) that
`write-state` receives as part of its own `In` and hands, unevaluated, to `gitstate.Write`. Nothing
else calls it. `gitstate.Write`'s retry loop re-fetches the state branch on each of its three
attempts, so whether this commit follows the branch's last-recorded one — and which findings
newly appeared or resolved since then — has to be answered against the branch as that attempt
just fetched it, not against a value computed once before the first attempt. See
[ADR 0069](../../docs/adr/0069-a-recordings-history-is-a-deferred-closure-and-its-inputs-cross-a-boundary-type.md)
for the decision and its rejected alternatives, and
[ADR 0070](../../docs/adr/0070-the-ledger-sink-composes-and-lands-history-and-record-decides-what-it-holds.md)
for the cut between `recordstages` and `ledgerstages` this section describes. `write-state` never
inspects the baseline snapshot it is handed alongside the records function — it passes `Snapshot`
to `gitstate.Write` unopened, because what a baseline holds is `decide-baseline`'s policy, not the
sink's to judge.

`write-state` is wired `.OnError(flow.RecordAndContinue)`, the one non-default policy in this
flow: a write that never lands is worth knowing about, but it must not undo the verdict
`decide-baseline` and `compose-records` already reached about what was being landed. `record.go`
reads it back through `res.Errors()` rather than unwrapping a returned error the way it does for
a `FailFlow` stage — `Result.Errors()` is where a `RecordAndContinue` stage's failure surfaces, one
`*flow.StageError` per stage that failed under that policy, while the flow itself still ran to
completion and returned no error of its own.

`recordstages.ReportReader` is this flow's boundary onto documents it does not own: the
measurements document (`lydite test`'s `measurementsDoc`), the mutant-counts document
(`lydite mutation`'s `mutantsDoc`, read and folded by `readMutants`/`foldMutants`) and the scan
document (`lydite scan`'s reporting) are each read and folded by the code that already owns that
logic, in `cmd/lydite`. `ReportReader` is a five-method interface — `ReadMeasurements`, `ReadScan`,
`ReadMutants`, `FoldMeasurements`, `FoldMutants` — typed entirely in `recordstages`' own boundary
types (`Measurements`, `Scan`, `Mutants`, each holding only the fields a stage reads), and
`record.go`'s adapter is the only thing that converts one command's document into another's stage
input. No `package main` type reaches `internal/stages/record`.

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
    reply. `mutationstages` (`internal/stages/mutation`) holds `lydite mutation`'s and `lydite
    mutation merge`'s own stages the same way.
  - `shardstages` (`internal/stages/shards`) is generic in the same sense as `truststages` and
    `scmstages`: any flow that folds a matrix of shards reads them through
    `shardstages.ReadShards`, not through a copy of its own.
- **`internal/flows/<command>`** — one package per command, holding that command's `New() (*flow.Flow, error)`, its `Params`, and the `Input`/`Stage` name constants a caller (today, only
  the CLI) reads a `flow.Result` back through. `mutationflow` (`internal/flows/mutation`) holds
  three: `New()` and `NewRecord()` for `lydite mutation`, and `NewMerge()` for `lydite mutation
  merge`.

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

### `test plan` and `test merge`

`testflow` holds two more flows beside `New()`: `NewPlan()` for `lydite test plan` and
`NewMerge()` for `lydite test merge`. Both are wired over stages in `teststages` that return data
and an unwrapped error only, never a `ui.Row` — a deliberate departure from `Declaration` and the
rest of `New()`'s own stages, which predate ADR 0065 and still return `Rows []ui.Row`.

`NewPlan()` runs `LoadPlanComponents` then `GroupShards`, the second `.When(Declared)`: a
declaration naming no component has nothing to group. `LoadPlanComponents` reads the declaration
alone — no configuration, no toolchain, no orphan gate — because a plan runs no suite.
`GroupShards` groups every component that declares a suite into the transitive closure of
`scheduler.Conflicts`, reading each compose file with `compose.NoRuntime` so grouping depends on
nothing but the declaration and the files beside it. A compose file that will not load, or two
shards that would take one name, fail the stage with the error that says so; `planError` unwraps
the flow's `*flow.StageError` back to that error, the same unwrap `release check`'s `ResolveTag`
performs for `ErrNoTag` (see "The release flow" below) to keep a stage's own words intact rather
than the flow's framing of them. The `--out` matrix's own row type, `matrixEntry`, stays in
`cmd/lydite/plan.go`: the stage returns plain shard-grouping data (`PlanShard`), and the CLI is
what turns it into the matrix's JSON shape.

`NewMerge()` runs `LoadMergeComponents`, then, both `.When(Declared)`, the generic
`shardstages.ReadShards` (`Command = flow.Literal("test")`) and `ReadShardMeasurements`.
`ReadShardMeasurements` never fails the flow as a whole: a shard's `measurements.json` that will
not parse becomes that shard's own `Err`, the same shape mutation's `ReadShardCounts` already
uses for a shard whose counts document will not parse (see "Mutation flows" below). It reads
through `teststages.MeasurementsReader` — a stage-owned interface, not a reuse of
`recordstages.ReportReader`, because that interface is a different concern's own boundary type.
`cmd/lydite`'s implementation, `shardMeasurements` (`cmd/lydite/measurements.go`), caches each real
`measurementsDoc` it reads, keyed by directory, mirroring `record.go`'s `recordReports`; the
stage's own `Out` carries only which directories were read and each shard's error, never the
document itself — the same shape the section below states for keeping that schema off a stage
boundary at all. No fold stage exists for `test merge`: `foldMeasured` and every row it renders
stay in `cmd/lydite/fold.go`, called directly the way `mutation_merge.go` already calls the same
package's row-fold helpers rather than duplicating them.

### `componentPlan`, `componentLog`, `measurementsDoc` and `componentMeasurement` stay in `cmd/lydite`

These four types are test/coverage logic by every other measure, and were left out of the move
anyway: `cmd/lydite/mutants.go`, `merge.go` and `record.go` reach into their *unexported fields and
methods* directly (`p.c`, `p.log`, `folded.snapshot()`, `e.asMeasurement(c)`), and no alias or
rename survives a type changing package: a field selector or a method call on a value of that type
breaks the moment the type is no longer the one declared where the caller's own compiler unit
sees it. This is the decision the rest of the migration is arranged around, and it generalises: a
future stage extraction should check, symbol by symbol, whether the calling file reaches a
*field or method* on a type (the type has to stay where it is) or merely *calls a function*
(safe to move behind a same-signature wrapper the CLI keeps).

`teststages.MeasurementsReader` is built to this same constraint rather than around it: the stage
never receives a `measurementsDoc` or a `componentMeasurement`, only whether a directory's document
was read and why not, and `shardMeasurements` is what keeps the real, unexported-field-bearing
values the composition still reaches into once the flow has returned. It is the general pattern
`recordstages.ReportReader` already establishes for `record` (above), not an exception carved out
for `test merge`.

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

## The review flow

`reviewflow.New` (`internal/flows/review`) declares `review`'s stages in this order:

| Stage | Conditions |
|---|---|
| `resolve-base` | none |
| `surfaces` | none — the guard bound to `Publish`, see below |
| `decide` | none |
| `check-dirty` | none |
| `init-trust` | `.When(publish).When(posts-directly)` |
| `init-scm` | `.When(publish).When(posts-directly)` |
| `load-pull-request` | `.When(publish)` |
| `compose-status` | `.When(publish)` |
| `render-status` | `.When(publish).When(renders-only)` |
| `post-status` | `.When(publish).When(posts-directly)` |

Every stage through `check-dirty` runs unconditionally: `resolve-base`, `surfaces` and `decide`
compute the comparison and the verdict a plain run reports, whether or not the run publishes
anything. `init-trust` and `init-scm` are declared late, after the decision, and conditioned on
`posts-directly` rather than on `publish` alone — a render-only run needs no credential at all,
and needs to read neither the environment nor an `SCMRepository`. A render-only or plain run
never reaches those stages and never asks for a credential; the decision and its warnings are
computed and available to the caller before any credential is asked for. Declaring trust and SCM
first, the way clearance does, would make a credential-less `--publish` run fail on a missing
token before it ever produced its warnings — see
[ADR 0071](../../docs/adr/0071-a-review-reads-its-pull-request-from-the-payload-and-posts-to-the-revision-it-measured.md)
for the full comparison against clearance's ordering and the rejected alternatives.

One consequence of that ordering: a missing or malformed `GITHUB_REPOSITORY` is reported in
`internal/trust`'s own words, ahead of a missing token, because `trust.FromEnvironment` checks
the repository before the token. The resulting error precedence for a direct-posting run is
repository → token → event. `trust` is frozen and returns untyped errors, so a trust failure is
passed through as it is rather than mapped back into a typed one the CLI's own wording could
describe — the only way to tell a missing repository from a malformed one apart would be
string-matching a frozen message, which is a boundary workaround.

`reviewflow.NewCompare` (`review-compare`) is `resolve-base` → `compare-surfaces` →
`write-surfaces`: resolve the base, compare every opted-in component against it, and write the
comparison as a document for a later `review` to read. Its `compare-surfaces` guard is bound to
`flow.Literal(false)`, not to an input — the job this flow runs in holds no publishing credential
yet to guard against, by construction, so there is nothing an input could usefully switch.

The verdict itself is decided once, in `reviewdecision.Decide`, as an ordered outcome list the
`decide` stage returns whole; `compose-status` composes the published `forge.Status` from that
same `Result.Verdict`, and the CLI only renders rows from the outcomes — see
[ADR 0072](../../docs/adr/0072-a-reviews-verdict-is-decided-once-in-reviewdecision-and-the-report-renders-it.md).
Publishing happens inside the flow, before the CLI renders anything: `compose-status` builds the
status, and `render-status`/`post-status` are the flow's own gated tail, one of the two ways a
run reaches an audience for a verdict the CLI has not yet turned into rows.

Posting directly and rendering for another step to post are alternatives a caller chooses
between, never a ladder: a repository that has not adopted the reusable workflows posts directly
and keeps every property of the status, and neither route is attempted after the other, because a
verdict published twice under two identities is the mixed record the App identity exists to end.
The status is the whole record a clearance acts on, and it has to land early — a person can start
clearing a referral while the test matrix is still running, which is the property
[ADR 0015](../../docs/adr/0015-clearance-binds-to-a-commit.md) rests on. The verdict also reaches
the pull request's standing comment, but by the route every other command's results take: the
report document the run wrote. Composing it again there would be a second derivation of one
answer, which is exactly what ADR 0072 rules out.

## The publish flow

`internal/flows/publish` (`publishflow`) and `internal/stages/publish` (`publishstages`) put
`lydite publish` on the same scaffold as `clearance`, and it is the case that shows the scaffold
costs nothing extra where a command has no platform, no credential, and no branching to speak of:
three stages, `gather-reports` → `build-comment` → `write-comment`, none conditioned on another's
output, every stage keeping the default `FailFlow`. `GatherReports` reads every named report
directory into its documents, or the reason it held none (`ReportDir.Missing`) — an unreadable or
empty directory is content the stage reports, never a `*StageError`. `BuildComment` folds what
`GatherReports` read into one rendered `ui.Comment`; `WriteComment` puts that comment on stdout or
in a file. Only `write-comment` can fail the flow at all.

Reading is a stage exactly like writing is: `GatherReports` is `LoadComment`'s counterpart for a
command with no platform to read from, and the "Source"/"Sink" framing the migration was proposed
in names nothing `internal/flow` actually distinguishes — see
[ADR 0074](../../docs/adr/0074-publish-runs-as-a-flow-of-ordinary-stages.md). What `GatherReports`
reads with (`ReadDocuments`) and what `BuildComment` reads a failing row's log with (`ReadLog`)
are both injected as `In` fields rather than imported — the functions stay defined in
`cmd/lydite/reports.go`, where other commands already call them, and the CLI passes them in as
flow inputs the same way `review` hands a stage `reviewdecision.Toolchains`. Which rows' logs get
read is an assembly decision `BuildComment` makes from each row's status and from `detailCap`, so
the reader is a field on `BuildIn` rather than something `GatherReports` resolves ahead of it.

## Mutation flows

`mutation` is built this way too, and unlike `clearance` it does not answer a webhook — it
reads the working tree, decides which components' mutants to run, and runs them.
`internal/flows/mutation` (`mutationflow`) declares three flows: `New()` for `lydite mutation`,
`NewRecord()` for the record it runs afterward, and `NewMerge()` for `lydite mutation merge`.

`New()` runs six stages in order — `LoadDeclaration`, `ProvisionToolchains`, `ResolveBase`,
`SelectAffected`, `ScopeChange`, `RunMutants` — the last five `.When(Declared)`, since a
declaration naming no component has nothing to provision, resolve or mutate. Every stage but
the last is a small, ordinary transform of the declaration into what the run needs before any
mutant executes; `RunMutants` is where the per-component work stays — `scheduler.Run`,
`mutation.Slots`, `mutation.Execute` — because that work is one scheduler dispatching each
component's whole lifecycle rather than a later stage reading an earlier one's output, and Flow
runs its stages once each, never in a loop and never concurrently (see "Why execution is
sequential" above). `RunMutants` answers with one `ComponentOutcome` per component, each naming
an `OutcomeKind` — one per distinct row `lydite mutation` can render, so two paths share a kind
only when their rows would be byte-identical — plus the facts that kind's row needs. It marks
`Scheduled` for what it handed to the scheduler and `Interrupted` for a run cancellation cut
short, and it never renders a row itself.

The CLI is the only place a `ComponentOutcome` becomes a `ui.Row`: `cmd/lydite/mutation.go`'s
`addMutationRows` walks the outcomes in declaration order, adds the select row, the schedule
row, one row per component (a skipped one interleaved where its declaration named it), every
survivor's finding, and the summary — and, when `RunMutants` reports the run interrupted, calls
`withdrawInterrupted` to pull back the failing verdict of every component the run had scheduled
but never got a real answer from, deciding what to withdraw from the row a gating run would have
rendered (`outcomeRow(o, false)`) rather than from the row this run displays, so `--no-gate`
withdraws exactly what a gating run would. Only the outcomes whose verdict still stands after
that withdrawal are handed to `NewRecord()`.

A lifecycle helper `lydite test` and `lydite mutation` share — `prepare`, `runCommands`,
`startServices`, each returning a decided `(ui.Row, bool)` (or, for the last, `(func(), ui.Row,
bool)`) — has nowhere type-safe to put that row across a stage boundary, since a stage's `In`
and `Out` never name `internal/ui`'s report type. The CLI's `mutationLifecycle` adapter wraps
the row in a `lifecycleRowError{row}` and returns it as a plain `error`; `mutationstages`
receives it as opaque, on `Planned.NotReady`, on a `KindBlocked` outcome, or on
`RunMutants.TeardownErr`, and hands it back unread. The CLI recovers the row with `errors.As`
only where the outcome kind says one was already decided (`KindBlocked`, a teardown);
`lifecycleRowError.Error()` is the row's detail joined by `"; "`, and a worker's preparation
failure reaches the report only through the executor's own error — `preparing the worker
directory: ` followed by that joined detail — which `KindExecuteFailed` renders as an
unmeasured row carrying the executor's text verbatim, never unwrapped with `errors.As`. See
[ADR 0065](../../docs/adr/0065-a-stage-reports-its-outcome-as-data-and-the-cli-alone-decides-the-rows.md)
and the rule it produced,
[`a-row-a-shared-helper-already-decided-crosses-into-a-stage-as-an-opaque-error.md`](../rules/a-row-a-shared-helper-already-decided-crosses-into-a-stage-as-an-opaque-error.md).

`NewRecord()` holds one stage, `RecordMutants`, in a flow of its own rather than as `New()`'s
seventh stage — `flow.Run` checks `ctx.Err()` before every stage it runs, and a flow that ran
`RecordMutants` as `New()`'s last stage would skip it on the same interrupt that cut
`RunMutants` short. The CLI runs `NewRecord()` on `context.WithoutCancel(ctx)` whenever
`RunMutants`' output is available at all, so a run cancelled mid-mutation still records the
components it finished before the signal arrived. The accepted residual: an interrupt landing
between two stages of `New()` itself — after `ScopeChange` and before `RunMutants`, say —
returns the bare context error rather than a "not run" row for every selected component, and
records nothing for that run. It is accepted because the window one `ctx.Err()` check leaves
open here is the same one every other flow leaves open between any two of its stages, not a
gap specific to mutation.

`NewMerge()` folds a matrix of shards for `lydite mutation merge`, in the order `LoadComponents`
(the declaration alone, no configuration), `shardstages.ReadShards` (`Command =
Literal("mutation")`), `ReadShardCounts`, `FoldShardCounts`, `ReadProjections` — the last four
`.When(Declared)`. Every stage past the first returns a problem as data rather than failing the
flow: a shard whose counts document will not parse is that shard's `Err`, and shards whose
counts disagree about the tree fold to an `Err` on the fold's own output, never a stage error.
The CLI composes the rendered rows afterward, in this order: shard inputs, the whole-tree
rows, the folded schedule row, the per-component rows, the folded mutation row, the carried
findings, the shards row.

### The shard domain value

Reading one report directory's document for one command is not specific to mutation:
`shardstages.ReadShards` (`internal/stages/shards`) is the generic stage both `lydite test
merge` and `lydite mutation merge` read their shards through, rather than each through its own
copy. What it returns — one `shard.Shard{Dir; Document ui.Document; Read bool; Err error}` per
directory — is a domain value in `internal/shard`, not a type either stage package owns,
because a type two stage packages both need is exactly the case that must never make one stage
package import another: `shardstages` produces `shard.Shard`, `mutationstages` consumes it in
its own `ReadShardCounts`, and the dependency arrow between the two stage packages stays absent.
This is the same principle `internal/trust`'s `TrustedContext` already establishes for
`truststages` and every stage downstream of it. See
[ADR 0066](../../docs/adr/0066-shard-documents-are-read-by-one-generic-stage-both-folds-share.md).

### A stage may read the report grammar, never write it

`internal/stages/clearance/reply.go`'s reply stage reads a `ui.Comment` as input, to compose the
next one; `shardstages` and `mutationstages`' `ReadShardCounts` read a `shard.Shard`, which
carries the shard's `ui.Document`, as input the same way. Neither builds a `ui.Row` or a
`ui.Report` — reading the grammar as data a caller supplied is not the same thing as deciding
what a report says, and every stage in this codebase does the first and never the second.
Deciding a row, in every flow built so far, is the CLI's job alone.

## The queue flow

`internal/flows/queue` (`queueflow`) and `internal/stages/queue` (`queuestages`) put `lydite
clearance queue` on the scaffold, over a new domain package, `internal/relay` — the pr-relay
client, transport and protocol only, importing nothing above it. Five stages, none conditioned on
another's output: `load-event` reads the entry the `merge_group` payload names; `resolve-base`
resolves the commit the decision is recomputed against; `recompute-decision` recomputes and
fingerprints that decision; `mint-token` mints the OIDC token the relay accepts, audienced to the
relay's own origin; `submit-comparison` submits the fingerprint for the relay to compare and
publish. `mint-token` is declared after `recompute-decision` so a run that cannot resolve the base
or recompute the decision fails for that reason before it fails for a missing mint endpoint. The
CLI reads `ACTIONS_ID_TOKEN_REQUEST_URL`/`_TOKEN` itself and passes them through as flow inputs,
empty or not; no stage reads the environment, and `truststages.InitTrust` stays the only one that
does anywhere in this codebase.

The flow declares no `init-trust` and no `init-scm`: this job is built to hold no writing
credential — `runQueue` refuses outright when `--relay` is empty, since there is no token to fall
back to — so there is neither a `TrustedContext` nor an `SCMRepository` for those stages to build.
That absence is why `load-event` reads the payload's `head_sha`, `head_ref` and `base_ref` as data
rather than a pointer resolved live: "trust and the repository come first" (see above, and ADR
0061) holds where a credential exists to resolve something live against, and this job has none.
The relay is what performs that live resolution instead, from the verified OIDC claim, checking
what this job submits against what it reads there itself. See
[ADR 0075](../../docs/adr/0075-the-merge-queue-submission-is-a-flow-over-a-relay-client.md) for
the full reasoning, including why this is not an `SCMRepository` implementation and not a
`RelaySink`.
## The threads flow

`threadsflow.New` (`internal/flows/threads`) declares `threads`'s stages in this order:

| Stage | Conditions |
|---|---|
| `init-trust` | none |
| `init-scm` | none |
| `load-pull-request` | none |
| `read-findings` | none |
| `list-threads` | none |
| `plan` | none |
| `write-ops` | none |
| `take-down` | `.When(apply)` |
| `answer` | `.When(apply)` |
| `open` | `.When(apply)` |

`init-trust` and `init-scm` are declared first, unconditionally — the opposite of the review
flow's ordering above. A `threads` run needs its `SCMRepository` from its very first read:
`list-threads` fetches the pull request's standing threads live even when `--apply` is not given,
because `plan` computes a delta against them, not against nothing. There is no point in this
flow's body where that need does not already hold, so trust is declared where the flow first
needs it — the same rule the review flow's late, conditional declaration follows for its own
premise. See [ADR 0073](../../docs/adr/0073-threads-declares-trust-first-and-writes-only-what-it-listed.md)
for the full comparison and the failure precedence this ordering produces: repository
(`init-trust`) → token (`init-scm`'s `scmstages.ErrNoCredential`) → event
(`load-pull-request`) → everything else. The CLI reads the repository slug for its own rows off
`init-trust`'s `Trusted.Repository()`, read from `flow.Output[truststages.Out](r,
threadsflow.StageInitTrust)`, rather than off the payload or a flag.

`read-findings`, `list-threads` and `plan` run unconditionally too: the plan and its warnings
(a duplicate fingerprint dropped, a report directory that could not be read) are computed and
written to `--ops` whether or not the caller asked to apply them, the same way `write-ops` always
runs. `take-down`, `answer` and `open` are the flow's gated tail, run only `.When(apply)`, after
the plan already exists — deleting the comments `Ops.Delete` names, replying to the ones
`Ops.Reply` leaves standing, and opening the ones `Ops.Create` names as new. Every id those three
stages write to comes from `Ops`, computed from `list-threads`'s own listing earlier in the same
run; no stage lists the pull request's comments a second time before writing.

`TakeDown` and `Open` each perform more than one write per call, and a `Result`'s `Out` is
unavailable once a stage has failed, so the progress each made before failing travels in a typed
error instead: `*threadsstages.TakeDownError{Answered []int64, Err error}` names every comment
answered instead of deleted before the failure, which is why the CLI still prints a warning for
each of those even when the run as a whole fails; `*threadsstages.OpenError{Lost, Posted int, Err
error}`'s `Error()` reads `"<Lost> located finding(s) reached no surface: <Err>"`. Both implement
`Unwrap`, so `errors.Is`/`errors.As` still reach the underlying cause.

## The release flow

`releaseflow.New` (`internal/flows/release`) declares `lydite release check`'s stages in this
order: `resolve-tag`, `previous-tag`, `read-commits`, `judge`. `internal/stages/release`
(`releasestages`) holds all four — `ResolveTag`, `PreviousTag`, `ReadCommits`, `Judge` — no
platform, no credential, and no webhook in the loop: the command reads a checkout's own tags and
commit messages and concludes whether a declared break lands on a bump that admits one, which is
business logic by [ADR 0076](../../docs/adr/0076-business-logic-runs-as-a-flow-and-the-cli-keeps-its-own-concerns.md)'s
definition even though it reads no platform live.

`resolve-tag` settles which tag is being released, in descending order of how explicitly it was
stated: a caller's own `--tag`, then the short name of the ref a run was triggered by (only when
that ref's type says it is a tag — a branch build's ref name is a branch, and checking it as a
version would report a misconfiguration as a malformed tag), then the tag the checkout itself is
on. `previous-tag` finds the release before it and runs unconditionally — a run with nothing to
check still needs to know whether this is the repository's first release. `read-commits` and
`judge` are declared `.Unless(flow.FromStage(StagePreviousTag, "First"))`: a first release has an
empty range, with no commits to read and nothing to judge. This is safe with no preceding guard,
for the reason the scan flow's `semgrep`/`secrets` pair already states for the same shape:
`previous-tag` runs under no condition of its own and the default `FailFlow`, so by the time
either later stage's condition is evaluated, `previous-tag` has either produced the `First` output
being read or already failed the run — there is no earlier condition to declare first, because
there is no way to reach the read with `previous-tag` having been skipped.

`ResolveTag` answers `releasestages.ErrNoTag` when neither the flag, the ref, nor the checkout
names a tag — a sentinel, not a worded message: a stage speaks in its own domain's terms and knows
nothing about how the CLI wants a person told to fix it. `runReleaseCheck` unwraps the flow's
`*flow.StageError` back to the stage's own error before comparing it against `ErrNoTag`, the same
unwrap every other command's top-level error handling performs to keep a stage's own error text
intact rather than reporting the flow's framing of it; only then does it supply the CLI's own
words naming `--tag`, `GITHUB_REF_NAME` and a checked-out tag as the three places one could have
come from.

The CLI picks the row from `previous-tag`'s and `judge`'s outputs in the same order the flow's own
conditions read them: `releaseRow` reads `previous-tag`'s `First` before it ever reads `judge`'s
output, because a first release's `judge` never ran and reading an unavailable stage's output
outside a flow's own guarded `Run` is exactly as unsafe as reading it from inside one. A first
release renders as an empty range that passed; declaring no break in the range renders as a pass
naming the range's size; a declared break the bump admits renders as a pass naming which commits
declared one; a declared break the bump does not admit renders as the one row this command exists
to produce, its detail closing with the rule the bump failed to satisfy.
