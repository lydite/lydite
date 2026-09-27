# A recording's history is a deferred closure across the write, and another command's document reaches a stage only through a boundary type

`lydite test record` is built on `internal/flow`: `internal/stages/record`
(`recordstages`) holds its stages, `internal/flows/record` (`recordflow`) wires them, and
`cmd/lydite/record.go` renders their outcomes. Two decisions about that shape are recorded here.

## Decision: `ComposeHistory`'s `Out` carries a closure, and only `WriteState` calls it

`gitstate.Write`'s retry loop re-fetches the state branch on each of its three attempts, because a
concurrent recording may have advanced it between fetches. Whether this commit follows the branch's
last-recorded one, and which findings newly appeared or resolved since then, are both questions
about the branch *as that attempt just fetched it* — an answer computed once, before the first
attempt, would have a later retry declare a gap or a resolved finding that an intervening run had
already filled. `ComposeHistoryOut.Records` is therefore a `gitstate.Records` function value, not
the records themselves, and `WriteState` is the only stage that ever calls it: it hands the
function straight to `gitstate.Write`, which invokes it once per attempt against the branch that
attempt fetched.

This is a stage's `Out` carrying something other than a fully computed value, which the ordinary
shape of a stage does not otherwise need — every other field in this flow is data, not behaviour.
Reflection cannot check what a closure does with its captured inputs the way `Build` checks a
binding, so the discipline that a stage's `In` is everything it reads holds up one layer removed:
`ComposeHistory` closes over values already bound into its own `In` (the folded measurements, the
finding counts, the mutant counts), and returns a function of those, rather than reaching for
anything outside them when it is eventually called.

### Rejected: splitting `gitstate.Write` into two writes

Composing the history and deciding the baseline are independent policies over one branch — a
partial baseline is refused while the same run's history is still appended — which might suggest
two separate writes, one per policy. Both land on the same branch in one commit today, and
splitting them would let a baseline land with no ledger record beside it, or vice versa, whenever
the second write's own retry lost a race the first write's did not: `WriteState` calls
`gitstate.Write` exactly once, with both the snapshot and the records function, and the retry loop
resolves the race for both together or neither. `gitstate` and `ledger` keep their existing
shape; splitting the write into two is a change to those packages, not to how `recordflow` calls
them, and nothing here decides it one way or the other.

### Rejected: a stage swallowing its own write error into `Out`

`WriteState` returns `gitstate.Write`'s error as its own error, rather than folding it into
`WriteStateOut` as a field the CLI reads. A stage does not pick its own `OnError` — that is the
flow definition's call (see [`agentic/references/architecture.md`](../../agentic/references/architecture.md)) —
so hiding the error inside `Out` would take that choice away from `recordflow.New` twice over: once
by deciding, inside the stage, that this error is "recorded, not fatal" (which is `recordflow`'s
`.OnError(flow.RecordAndContinue)`, not the stage's to assume), and again by making every future
flow that might reuse `WriteState` inherit that same assumption whether it wants `RecordAndContinue`
or not.

## Decision: another command's document reaches a stage only through a stage-owned boundary type

`recordstages` never imports a `package main` type. The measurements document (`measurementsDoc`,
owned by `lydite test`), the mutant-counts document (owned by `lydite mutation`'s `mutantsDoc`,
read and folded by `readMutants`/`foldMutants`), and the scan document
(owned by `lydite scan`'s reporting) are each read, folded, and — for measurements —
snapshotted by the code that already owns that logic, in `cmd/lydite`. `recordstages.ReportReader`
is the seam: an interface of five methods (`ReadMeasurements`, `ReadScan`, `ReadMutants`,
`FoldMeasurements`, `FoldMutants`) typed entirely in `recordstages`' own boundary types
(`Measurements`, `Scan`, `Mutants`), each holding only the fields a stage actually reads. The
CLI implements `ReportReader` by calling the owning code and converting its answer; every read
error passes through the conversion unchanged, so a missing document's `os.ErrNotExist` text
reaches a row byte for byte.

### Rejected: typing a stage on the owning command's own document type

A stage could instead read `measurementsDoc` or `mutantsDoc` directly, importing the type its
owning command declares rather than a boundary type of its own. Neither document's owner lives
in an importable package: both types are declared inside `cmd/lydite`, `package main`, which no
stage can import at all. Even where that were not so, a document's owning type changes on its
owner's own schedule, for its owner's own reasons — a stage typed on it directly changes every
time the owner does, whether or not the fields a stage reads moved. A boundary type holding only
the fields a stage actually reads costs little to declare, and when the owning type changes, only
the CLI adapter that fills the boundary type changes — the stage does not.

### Rejected: test-only forwarding wrappers

An earlier shape considered keeping `historyRecords` and `readReports` in `cmd/lydite` as thin
wrappers over the relocated logic, purely so their existing tests would not have to move. Rejected:
the tests protect the logic, not the name of the function holding it, and a wrapper that exists
only to keep an old test compiling is a permanent, pointless indirection once the test sits beside
the package that owns what it is testing.

## Consequences

- A stage's `Out` may carry a function rather than a value, when — and only when — the question
  the function answers depends on state a caller reads fresh at the moment it is called, and the
  flow guarantees there is exactly one such call site.
- Any future command that folds one command's report document into another's flow follows the same
  seam: a stage-owned interface and stage-owned boundary types, never a `package main` import
  reaching down into `internal/stages/*`.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md) for the four
layers this command is built on, and
[`agentic/references/quality-history.md`](../../agentic/references/quality-history.md) for
`gitstate.Write`'s retry loop and why `ComposeHistory` and `DecideBaseline` are independent
policies over the same branch.
