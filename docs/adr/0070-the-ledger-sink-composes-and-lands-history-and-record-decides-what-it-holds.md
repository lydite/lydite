# The ledger sink composes and lands history; record decides what it holds

`recordflow` (`internal/flows/record`) splits `lydite test record`'s last four stages across two
packages that do not import each other: `recordstages` (`internal/stages/record`) decides what a
recording measured, and `ledgerstages` (`internal/stages/ledger`) decides how a quality history is
composed from that and landed on the state branch. This records the cut between them and why the
flow, not either package, is what binds one's output to the other's input.

## Decision: record decides what was measured; ledger decides how history is composed and landed

`ComposeLedgerInputs`, in `recordstages`, turns what a recording folded — the measurements, the
per-component and root-scoped finding counts, the mutant counts, and the scan's own crashed
buckets — into the ledger's own vocabulary: a `map[string]ledger.Component` per component's
scalars, the root-scoped finding counts, and the set of finding buckets this recording actually
measured. Which components carried a number, and which bucket a crash removes from scope, are
questions about the documents a recording folds, and only `recordstages` reads those documents at
all.

`ComposeRecords` and `WriteState`, in `ledgerstages`, take those three values as plain data and do
everything from there: resolving the branch and the commit a record is filed against, diffing the
findings in scope against what the branch holds open, marking a gap ahead of a commit whose parent
was never recorded, and landing the result beside whatever baseline `DecideBaseline` computed.
Neither of those questions needs to know a report document exists — `ledgerstages` takes
`map[string]ledger.Component`, `map[string]int` and `map[ledger.FindingBucket]bool`, never a
`recordstages.Measurements` or a `finding.Crash`. The package doc for `ledgerstages` states this
directly: what a recording measured, and which bucket it measured, is the caller's to decide, and
arrives in the ledger's own vocabulary.

Neither package imports the other. `recordflow` is what binds `ComposeLedgerInputs`'s `Out` to
`ComposeRecords`'s `In` — a plain `flow.FromStage` reference, the same shape every other value in
this flow crosses a stage boundary by. A future caller of `ledgerstages` that measures something
`recordstages` never folds — a second command with its own report documents — supplies the same
three values from wherever it reads them, and neither `ledgerstages` nor its tests change.

## Decision: `WriteState` never inspects the baseline it lands

`WriteStateIn.Snapshot` is a `gitstate.Snapshot`, passed to `gitstate.Write` exactly as `WriteState`
received it. `WriteState` does not read a field of it, branch on whether it is empty, or log
anything about what it holds — what a baseline holds, and whether there is one to land at all, is
`DecideBaseline`'s policy, decided before `WriteState` ever runs. `WriteState`'s own job is the one
call that reaches the state branch, for whichever of the baseline and the history has something to
say, and treating the snapshot as opaque data keeps that job exactly that: a sink, not a second
place a baseline's shape gets judged.

## Rejected: keeping the write in `recordstages` and moving only composition

An earlier shape moved `ComposeRecords` into `ledgerstages` but left `WriteState` where
`recordstages.WriteState` already was, so that only the composing logic changed package. Rejected:
that would make `ledgerstages` a package that composes a history and never lands one, and leave the
one call to `gitstate.Write` inside a package named for what a command records rather than for what
the ledger is — exactly the split this ADR exists to avoid. `gitstate.Write` has exactly one
production caller, `internal/stages/ledger/write.go`, and `recordstages` holds no
`gitstate.Write`, `ledger.BranchState`, or gap-detection logic of its own.

## Rejected: moving `findingScope`'s crashed-bucket policy into ledger

`findingScope` decides which finding bucket a recording measured by reading the scan document's
own list of crashed gates, and removing a crashed bucket from scope rather than letting a crash's
zero count read as a clean run. Moving that decision into `ledgerstages` would mean passing
`[]finding.Crash` in alongside the scope it already computes, which is exactly the kind of document
knowledge this cut keeps out of `ledgerstages`: which buckets a run measured, and why a bucket
that crashed does not count as measured, is record's policy over its own documents, not something
the ledger sink needs to re-derive.

## The deferred closure and the boundary-type seam are unchanged, and are not restated here

`ComposeRecords`'s `Out` still carries a `gitstate.Records` function rather than the records
themselves, called exactly once, inside `WriteState`, against the branch that write's own attempt
just fetched — the reason is the same one recorded for `ComposeHistory` before this split, and the
one-write, one-call-site invariant it protects does not change because the function now closes over
`ledgerstages`' own inputs instead of `recordstages`'. See
[ADR 0069](0069-a-recordings-history-is-a-deferred-closure-and-its-inputs-cross-a-boundary-type.md)
for that decision, its rejected alternatives, and the boundary-type seam `recordstages.ReportReader`
still is between `recordstages` and the documents `lydite test`, `lydite mutation` and `lydite scan`
each own — neither of which this split touches.

## Consequences

- `internal/stages/record` contains no `gitstate.Write`, `ledger.BranchState`, or gap-detection
  logic. `internal/stages/ledger` contains no report-document type and no crashed-bucket policy.
- A future stage that lands a second kind of history on the state branch reuses `ledgerstages.
  ComposeRecords` and `WriteState` directly, supplying the three ledger-vocabulary values from
  whatever it measures, rather than growing a second copy of the branch resolution, the gap
  detection, or the one write.
- `Reason` (`NoBranch`, `NoScalars`, `Undescribable`) is `ledgerstages`' own kind, never text: the
  CLI's existing mapping renders the wording, exactly as it did for `recordstages`' own `Reason`
  before this split.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md) for the four
layers `record` is built on, and where this split sits inside them.
