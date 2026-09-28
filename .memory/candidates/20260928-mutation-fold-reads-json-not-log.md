---
about: the mutation shard fold's completeness check reads only <dir>/mutation.json; mutation.log is read separately, only for the cost projection, and is invisible to completeness
saw:
  - source/cli/internal/shard/shard.go
  - source/cli/cmd/lydite/fold.go
  - source/cli/internal/stages/mutation/merge.go
  - source/cli/cmd/lydite/mutation_merge.go
  - agentic/references/mutation.md
  - docs/adr/0027-mutation-is-its-own-command.md
---

Explored while assessing whether a mutation run could write a partial `mutation.json` on
SIGINT/SIGTERM so the fold would still see the component as covered, instead of leaking an
orphaned child (see the `executil-runto-no-process-group-kill` candidate for that side).

`shard.Read(dir, "mutation")` (shard.go:37-49) opens exactly `<dir>/mutation.json` — the
document is located by filename alone, its contents never compared to what it claims to be.
`cmd/lydite/fold.go`'s `shardInputs` (fold.go:77-99) adds a `StatusFail` "no mutation report"
row for a shard where `shard.Read` is false, and that row is what makes `shardsRow`
(fold.go:354-365) fail the whole `shards` verdict when a declared component has zero rows
across all shards. `mutation.log` is never consulted for completeness at all — `merge.go`'s
`shardProjection` (merge.go:188-221) reads it only to recover the `N mutant(s), budget Xs
each...` projection line for the summary; a shard with a log but no `mutation.json` is treated
exactly as if it wrote nothing.

Consequence verified: **any well-formed `ui.Document` written to `mutation.json` satisfies the
completeness check, regardless of its row's Status or Value text.** A component row with
`Status: StatusFail, Value: "interrupted: N of M mutants left..."` would be accepted as that
component's one-and-only row — it would *not* trigger the "no mutation report" failure, because
`shardInputs` only checks `shard.Read` (did the JSON parse), never the row's content.

`mutation_merge.go`'s `killedOf` regex (line 254,
`^(\d+) of (\d+) mutant\(s\) (killed|survived) in .+$`) is the only thing that reads a row's
*prose* back out for the folded score, used only when a shard wrote no `mutants.json`
(`foldedMutationRow`, mutation_merge.go:276+). A row worded "interrupted: N of M mutants left,
in flight: X" would not match that pattern, so it would count toward completeness (one row per
component) but contribute nothing to the summed killed/total score — silently, the same way any
other non-matching wording does today.

`agentic/references/mutation.md`'s "no runtime budget" section (lines 368-374) states the
current, deliberate design this would change: "A run genuinely too large dies as a CI job
timeout, the shard produces no document, and the fold already fails a declared component with
no row." Making mutation write a document on interrupt is a considered change to that stated
invariant, not a bug fix within it — worth flagging explicitly to whoever reviews the fix.
