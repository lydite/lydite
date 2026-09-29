---
name: record-flow-tree-mismatch-is-an-answer-not-an-error
kind: invariant
description: In the record flow a tree mismatch is BindTree's answer (Bound=false), not an error, and every stage after it runs only when bound, so a stage added after bind-tree must also be conditioned on it.
anchors:
  - path: source/cli/internal/flows/record/record.go
    blob: 54ff900376ba
  - path: source/cli/internal/stages/record/tree.go
    blob: 1b447b822cc6
confidence: verified
---

`BindTree` (`internal/stages/record/tree.go:43`) returns an output whose `Bound` (`tree.go:23`) says whether the folded measurements' tree is the tree at `HEAD`; it errors only when git cannot resolve the tree. `recordflow.New` (`internal/flows/record/record.go`) declares `load-declaration`, `read-reports`, `fold-measurements` and `bind-tree` unconditionally (default fail-the-flow), and every later stage — `count-findings`, `bind-mutants`, `compose-ledger-inputs`, `compose-records`, `decide-baseline`, `write-state` — carries `.When(bound)` (lines ~144-181). On a mismatch all of them are skipped, and the CLI must check `Bound` before reading any skipped stage's output, because `flow.Output` on a skipped stage is an error (`cmd/lydite/record.go`'s `recordRows`). A stage added after `bind-tree` that files anything against the tree must also run `.When(bound)`, or it records one tree's numbers under another's. `write-state` is `.OnError(flow.RecordAndContinue)` (`record.go:186`), so a failed write becomes a row, not a flow failure.
