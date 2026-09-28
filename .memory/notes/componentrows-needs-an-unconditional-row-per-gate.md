---
name: componentrows-needs-an-unconditional-row-per-gate
kind: invariant
description: a new per-component gate row must be emitted unconditionally by every shard, even when the gate flag wasn't passed, because componentRows's fold expects exactly one row per label per declared component and reads zero as a dead shard.
anchors:
  - path: source/cli/cmd/lydite/fold.go
    blob: 454c64d57793
  - path: source/cli/cmd/lydite/merge.go
    blob: 4f0a5cea19eb
  - path: source/cli/cmd/lydite/mutation_merge.go
    blob: 94a878f61893
  - path: source/cli/cmd/lydite/mutation_merge_test.go
    blob: 4844fae14904
  - path: source/cli/internal/test/run/flaky.go
    blob: c88bf1fab573
  - path: source/cli/internal/stages/test/flaky.go
    blob: d6a9d36f7a3d
confidence: verified
---

`componentRows`/`componentRowsNoting` (`fold.go:276,289`) fold a per-component row
across shards by requiring exactly one row under that label from exactly one shard —
zero is read as "a shard whose job died," two as "two jobs running the same work."

The `--gate-flaky` gate's `flaky(<name>)` row is folded the same way (`merge.go`, via
`suiteRows(rep, decl, inputs, flakyLabel, noSuiteFlakyRow)`), which means every shard
has to emit a `flaky(<name>)` row for every component it owns *whether or not
`--gate-flaky` was passed*. `internal/test/run/flaky.go`'s `FlakyGate.Report` gives a
component with no examined row a `context` row ("not gated — --gate-flaky reruns the
tests a change introduces") when the gate wasn't requested, the same way
`coverage(<name>)` is always present; `internal/stages/test/flaky.go`'s `FlakyGate`
stage renders it on every run. A row emitted only when the gate is requested would make
`componentRows` see zero rows for that component on an ungated run and misreport it as a
dead shard.

Smaller gotcha: `componentRowsNoting`'s own problem message (`fold.go:295`, "`<name> has
no row in any shard's report`") doesn't name which label was missing — it takes one
`label func(string) string` per call and is called once per gate kind, so folding a
second gate's rows through it produces a problem string indistinguishable from another
gate's if both are missing. `merge.go` works around this by prefixing `"flaky: "` onto
that call's own returned problem strings rather than changing the shared helper, whose
message text `mutation_merge_test.go` asserts verbatim.

`componentRows` is a thin wrapper over `componentRowsNoting`, which takes an extra
`note func(string) string` appended after the same sentence — `mutation_merge.go`'s
`mutationRows` is the one caller that passes one (`projectionNote`); every other caller
gets the sentence with nothing appended.

A new gate row added without this unconditional-context-row treatment will silently
break `componentRows`'s dead-shard detection the first time the gate isn't requested.
