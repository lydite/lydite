# `lydite publish` runs as a Flow of ordinary stages

`clearance` piloted `internal/flow` for a command with a platform, a token, trust and branching —
every property this architecture exists to keep in check. `lydite publish` has none of them: it
reads report documents off local disk, folds them into one rendered comment, and writes that
comment to stdout or a file, all in-process with no network call. `internal/flows/publish`
(package `publishflow`) and `internal/stages/publish` (package `publishstages`) put it on the same
scaffold anyway — `gather-reports`, `build-comment` and `write-comment`, wired by `publishflow.New`
exactly as `clearanceflow.New` wires its own stages. This records why, and what the framing the
migration was proposed in — "Source" and "Sink" — does not mean here.

## Three ordinary stages, not a Source and a Sink

The issue that proposed this migration described `publish` as needing a `Source` counterpart to
the pilot's `Sink` — reading N report directories as one kind of stage, writing the comment as
another. No `Sink` type exists anywhere in `internal/flow` or the clearance pilot; what the pilot
actually built is `scmstages.LoadComment` (a stage that reads) beside `clearancestages.
RenderStatuses` (a stage that writes), each a plain `func(context.Context, In) (Out, error)` like
every other stage. `internal/flow`'s contract draws no line between a stage that reads, one that
computes and one that writes — a stage is a function of its `In`, whatever it does with it. So
`publish`'s reading step is `GatherReports`, one stage among three, not a distinguished kind of
node the engine has to know about.

Reading N documents is one call to `GatherReports` because there is one directory list, not N
stages — an unreadable or empty directory is content the stage reports (`ReportDir.Missing`), not
a `*StageError`; the "no report from this input" line a reader sees today comes out of `Gathered`
exactly as it does now, and only `write-comment`'s two failure modes (an unwritable file, a bad
parent directory) can fail the flow at all.

## Reading and writing are injected, never imported

`readDocuments` and `readLog` stay defined in `cmd/lydite/reports.go`, where `threads.go`,
`fold.go`, `record.go` and `review_depdelta.go` already call them. `publishstages` declares
`ReadDocuments func(dir string) ([]ui.Document, error)` and `ReadLog func(dir, rel string)
[]string`, and the CLI passes the existing functions in as flow inputs — the same shape `review`'s
`reviewdecision.Toolchains` already uses to hand a stage something the CLI owns without the stage
importing `cmd/lydite`. `internal/flow` stays frozen and untouched: nothing about wiring a reader
in as an input needed a new engine concept, because `flow.Build`'s reflection check already
assigns an unnamed func value to a named func type the same way it assigns any other binding.

## `build-comment` reads a log through its own injected reader

`GatherReports` could, in principle, read every row's log while it reads a directory's documents.
It does not: which rows' logs get read depends on each row's status (only a failing or referred
row quotes one) and on `detailCap` (only the first five of those), both assembly decisions
`BuildComment` makes while it folds directories into sections — not decisions `GatherReports` can
make without duplicating `BuildComment`'s own selection, or reading logs for rows nothing ever
shows. So `ReadLog` is a field on `BuildIn`, read where the row that needs it is already in hand,
and `BuildComment` stays a function of nothing but its own `In` — the reader is data threaded in,
not the stage reaching for something outside it.

`ReadLog` (the CLI's `readLog`) cuts a log through `tail()`, which is `testrun.Tail` bounded by
`testrun.TailLines`; `BuildIn.TailLines` bounds something else — how many unanchored claims
`detailFor` lists for one row. The two agree only because the CLI fills `TailLines` from its own
`tailLines` constant, which is `testrun.TailLines` — one source of truth for the number, not one
bound deriving from the other. A private copy of the number in `publishstages` could still drift
from the reader's the moment either one changes on its own, so `TailLines` is an input rather than
a constant of its own.

## The `buildComment` shim runs the flow, not a stage around it

`cmd/lydite/publish.go` keeps a `buildComment(dirs []string, base string, expect ...string)
ui.Comment` function because `mutation_test.go` calls it by that name and signature. It builds and
runs `publishflow.New()` with `Out: "-"` and `Stdout: io.Discard`, and reads the comment back off
`flow.Output[publishstages.BuildOut]` — the same flow the command itself runs, not a bare call
into `BuildComment` that would let the shim and the command disagree about what a run does. Every
error the shim can observe (`New`, `Run`, `Output`) means the flow is wired wrong, not that a
directory could not be read — an unreadable directory is a section of the comment, never an error
this shim would see — so it panics rather than return an empty comment a caller would read as a
render that legitimately produced nothing.

## Rejected: leave `publish` a plain Cobra command

`publish` has no platform call, no credential, no branching on a webhook payload — none of the
properties the pilot needed the engine for. Staying a plain command, with its helpers as ordinary
package-level functions, was considered and would have cost nothing in capability. It was rejected
for structural consistency with the commands already built on the scaffold, chosen deliberately
even though `publish` needs none of what the engine was built for — a command with no platform to
speak to is exactly the case that shows the scaffold does not cost anything extra to adopt where a
flow's stages need no condition and no policy beyond the default. Reading and writing already had to be *something* in
this architecture's terms — ordinary stages, once the Source/Sink framing turned out not to exist
— and once they are, wiring them through `flow.New` is no more code than wiring them through a
`RunE` body directly.

## Rejected: a Source/Sink engine abstraction

A second alternative was building the `Source`/`Sink` distinction the issue assumed already
existed — a new engine concept for "a stage that reads" and "a stage that writes," so `publish`'s
shape could be described in those terms literally. This was rejected because `internal/flow` is
frozen, and because it is a distinction the architecture draws nowhere else: `scmstages.
LoadComment` reads, `clearancestages.RenderStatuses` writes, and neither is typed any differently
from `clearancestages.Decide`, which does neither. Building the distinction for `publish` alone
would give one flow a shape no other flow shares, for a question — "is this stage reading or
writing" — the engine has never needed answered to run one.

## Consequences

- `publishstages` imports `internal/ui` (`Document`, `Comment`, `CommentSection`, `Row`, `Status`,
  `Verdict`) and `internal/finding`, and nothing from `cmd/lydite` or `ui.Report` — the layering
  rule that a stage is callable outside a `cobra` invocation holds for a command with no platform
  the same way it holds for `clearance`.
- A command with nothing to read or write from a hosting platform is still three stages, because
  "read," "assemble," and "write" are three responsibilities regardless of where the data comes
  from — the same three-stage shape a future purely computational command would reach for.
- The Source/Sink language in the originating issue does not name anything real in this codebase
  and should not be repeated as though it did; the correct description is "stages, some of which
  read and some of which write."

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md) for the
publish flow among the four layers, and
[ADR 0059](0059-a-flow-is-a-hand-rolled-engine-of-typed-bindings-not-a-pipeline-library-or-a-shared-context.md)
for why the engine itself is frozen rather than grown a new concept for this.
