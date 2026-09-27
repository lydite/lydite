# Shard documents are read by one generic stage both folds share

`lydite test merge` and `lydite mutation merge` each fold a matrix of shards: every shard was
responsible for a subset of the declaration, and reports exactly that subset in a document one
report directory carries. Before either fold can say anything about a declared component with
no row, or a missing document, it has to read one document — `test.json` for one fold,
`mutation.json` for the other — out of each `--reports` directory. `internal/stages/shards`
(`shardstages`) holds that read as one stage, `ReadShards`, and `internal/shard` holds the
domain value it produces: `Shard{Dir; Document ui.Document; Read bool; Err error}`, and the
`Read(dir, command string) Shard` that finds a directory's document by name alone.
`mutationstages` consumes `shard.Shard` in its own `ReadShardCounts`, and no stage package
imports another.

## Rejected: a reader copy per command

`cmd/lydite/fold.go`'s `readShards` is `lydite test merge`'s own function, called from
`merge.go`, and the natural first move for `lydite mutation merge` would be to write a second
copy inside `internal/stages/mutation` — same loop, same `documentPath`/`readDocument` pair,
typed against `mutation`'s own literals instead of `test`'s. It is rejected for the reason
`mutation.md`'s own fold section already states about
`cmd/lydite/fold.go` itself: "that rule lives in `fold.go` with two consumers rather than in two
copies that agree until one learns something." A second copy of the read is exactly the same
risk one level lower — it agrees with the first until one of them changes how a missing document
is told apart from an unreadable one, and nothing catches the drift because the two copies never
run against the same test.

## Rejected: `mutationstages` importing `shardstages` for the `Shard` type

The shape `internal/shard` exists to rule out is a stage package importing another stage
package to reuse a type. `shardstages.ReadShards` returns `[]shard.Shard`, and it would read as
harmless for `mutationstages.ReadShardCounts` to import `internal/stages/shards` and take that
slice as its own input type, since `mutationflow.NewMerge` already wires `ReadShards`' output
into `ReadShardCounts`'s input. It is rejected because `internal/stages/<concern>` is defined,
throughout the architecture, as one package per concern that a flow definition wires together —
never a package another stage package reaches into for a shared type. A type two stage packages
both need to talk about belongs in a domain package instead, the way `internal/trust`'s
`TrustedContext` is read by every stage that needs one without any of those stages importing
each other. `shard.Shard` is that domain value for a shard's document: `shardstages` produces
it, `mutationstages` consumes it, and the dependency arrow between the two stage packages stays
absent rather than one-way.

## Rejected: parallel slices of directories and read flags

A narrower alternative kept the read as a plain function rather than a `shard.Shard` value, and
had `ReadShardCounts` (and `lydite test merge`'s own fold) take the same information back as two
or three parallel slices — the directories, which of them were read, the errors for the ones
that weren't — indexed by position instead of carried in one struct per shard. It is rejected
because a parallel slice is exactly the shape a later edit desynchronises silently: appending to
one slice and not its partner compiles, and the mismatch only shows up as the wrong shard's
directory attached to the wrong error, at whatever position the append happened to land on. One
`Shard` value per directory, in `Reports`' order, keeps a shard's directory, its document, and
why it wasn't read as one unit that cannot drift apart from itself.

## Consequences

- `internal/shard`'s `documentPath`/`readDocument` are the one implementation both
  `shardstages.ReadShards` and `lydite test merge`'s own reading go through. A
  package-main test in `cmd/lydite` pins the CLI's own `saveDocument` against
  `internal/shard`'s copy, so the two cannot drift on where a report document is written versus
  where it is read back from.
- `cmd/lydite`'s `shardInputs` (mutation) and the equivalent in `lydite test merge` still decide
  the row a shard's own read earns — findings first, then the row, then the fold's own hook —
  because `ReadShards` returns data and nothing else. Deciding what an unreadable shard's row
  says, and in what order the fold composes rows around it, is the CLI's job exactly as deciding
  any other row is.
- `cmd/lydite` imports `internal/shard` under the alias `shardreport`, because `plan.go` already
  declares an unrelated `shard` type for `lydite test plan`'s own sharding, and the two names
  would otherwise collide on import.

See [`agentic/references/architecture.md`](../../agentic/references/architecture.md)'s "Mutation
flows" section for where `shardstages` and `internal/shard` sit among the four layers, and
[ADR 0065](0065-a-stage-reports-its-outcome-as-data-and-the-cli-alone-decides-the-rows.md) for
the same "a stage returns data, the CLI decides the row" reasoning applied to what a fold does
once it has read its shards.
