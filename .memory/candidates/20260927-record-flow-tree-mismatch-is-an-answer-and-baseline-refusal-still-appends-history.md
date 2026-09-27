---
about: in the record flow, a tree mismatch is BindTree's answer (Bound=false), not an error, and every later stage runs .When(bound); DecideBaseline's verdict and ComposeHistory are independent, so a refused (partial) baseline still appends the run's history in the same single write
saw:
  - source/cli/internal/flows/record/record.go
  - source/cli/internal/stages/record/tree.go
  - source/cli/internal/stages/record/baseline.go
  - source/cli/internal/stages/record/history.go
  - source/cli/cmd/lydite/record.go
---

**Mismatch is an answer.** `BindTree` (`internal/stages/record/tree.go`) returns
`BindTreeOut{Bound: head == in.Folded.Tree, Head, Measured}`; it errors only when
`gitstate.TreeSHA(ctx, dir, "HEAD")` fails. `recordflow.New` (`internal/flows/record/record.go`)
declares `load-declaration`, `read-reports`, `fold-measurements` and `bind-tree` unconditionally
(default `FailFlow`), and every later stage — `count-findings`, `bind-mutants`, `compose-history`,
`decide-baseline`, `write-state` — carries `.When(bound)` and no other condition. Because
`bind-tree` itself has no condition and fails the whole run on error, a stage reaching the
`bound` condition always finds `bind-tree`'s output, so the ordering rule (guard a `FromStage`
condition with the one implying the stage ran) needs no extra guard here. On a mismatch all five
later stages are skipped; `cmd/lydite/record.go`'s `recordRows` checks `bound.Bound` before
reading any skipped stage's output (`flow.Output` on a skipped stage is an error) and renders one
failing `record` row naming both trees. A new stage added after `bind-tree` that files anything
against the tree must also run `.When(bound)`, or it records one tree's numbers under another's.

**Baseline verdict and history are independent.** `DecideBaseline`
(`internal/stages/record/baseline.go`) answers with a `Verdict` — `VerdictToRecord`,
`VerdictUnchanged`, `VerdictRefused` (a declared component missing from the fold, via
`MissingFromRecord`) or `VerdictNothingToRecord` — and an empty `Snapshot` for the last two; a
refusal is not an error. `ComposeHistory` (`history.go`) runs before it and reads none of its
output (it takes `Folded`, finding counts, `Found`/`Crashed`, mutant counts). `WriteState` then
lands `Snapshot` and `Records` in one `gitstate.Write` commit, so a refused partial baseline with
an empty snapshot still appends the history record. Rationale in `DecideBaseline`'s doc: a
baseline is a cache, refused when partial because a partial one gates later changes on nothing;
a record is not recomputable and is appended regardless. `recordRows` mirrors this: a failed write
is a failing `record` row only when `decided.Snapshot.Recorded() || decided.Verdict ==
VerdictToRecord`; a history-only write failure is an amber `history` row.
