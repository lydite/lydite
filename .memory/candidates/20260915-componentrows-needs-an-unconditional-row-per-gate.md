---
about: adding a new per-component gate row (beyond test/coverage/patch/crap/floor) requires emitting it unconditionally, in every shard, even when the gate flag was not passed — componentRows folds by counting exactly one row per label per declared component
saw:
  - source/cli/cmd/lydite/fold.go
  - source/cli/cmd/lydite/merge.go
  - source/cli/cmd/lydite/mutation_merge.go
  - source/cli/cmd/lydite/mutation_merge_test.go
  - source/cli/internal/test/run/flaky.go
  - source/cli/internal/stages/test/flaky.go
---

`fold.go`'s `componentRows` (and the `rowsFor`/`carryUnhandled` machinery around it) folds a
per-component row across shards by requiring exactly one row under that label from exactly one
shard — zero is "a shard whose job died," two is "two jobs running the same work." The
`--gate-flaky` gate's `flaky(<name>)` row is folded the same way (`merge.go` line ~191,
`suiteRows(rep, decl, inputs, flakyLabel, noSuiteFlakyRow)`), which means every shard has to
emit a `flaky(<name>)` row for every component it owns *whether or not `--gate-flaky` was
passed*. `internal/test/run/flaky.go`'s `FlakyGate.Report(own)` does: a component with no
examined row gets a `context` row ("not gated — --gate-flaky reruns the tests a change
introduces") when the gate was not requested, the same way `coverage(<name>)` is always present;
`internal/stages/test/flaky.go`'s `FlakyGate` stage renders it on every run. A row emitted only
when the gate is requested would make `componentRows` see zero rows for that component on an
ungated run and report it as a dead shard.

A second, smaller gotcha: `componentRowsNoting`'s own problem message (`fold.go` line ~276,
"`<name> has no row in any shard's report`") does not name which label was missing, because it
takes one `label func(string) string` per call and is called once per gate kind. Folding a second
gate's rows through it produces a problem string indistinguishable from the other gate's if both
are missing — `merge.go` works around this by prefixing `"flaky: "` onto that call's returned
problem strings (line ~192) rather than changing the shared helper, whose message text
`mutation_merge_test.go` asserts verbatim (lines ~172 and ~183).

`componentRows` is a thin wrapper over `componentRowsNoting`, which takes an extra
`note func(string) string` and appends a non-empty note after the same sentence.
`mutation_merge.go`'s `mutationRows` is the one caller that passes one (`projectionNote`); every
other caller gets the sentence with nothing appended.
