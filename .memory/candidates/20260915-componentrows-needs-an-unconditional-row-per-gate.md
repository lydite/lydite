---
about: adding a new per-component gate row (beyond test/coverage/patch/crap/floor) requires emitting it unconditionally, in every shard, even when the gate flag was not passed — componentRows folds by counting exactly one row per label per declared component
saw:
  - source/cli/cmd/lydite/fold.go
  - source/cli/cmd/lydite/merge.go
  - source/cli/cmd/lydite/test.go
---

`fold.go`'s `componentRows` (and the `foldedRow`/`carryUnhandled` machinery around it) folds a
per-component row across shards by requiring exactly one row under that label from exactly one
shard — zero is "a shard whose job died," two is "two jobs running the same work." Adding
`--gate-flaky`'s `flaky(<name>)` row (`merge.go`, `componentRows(rep, decl, inputs, flakyLabel)`)
meant every shard has to emit a `flaky(<name>)` row for every component it owns *whether or not
`--gate-flaky` was passed* — a `context` row ("not gated") when the flag is off, the same way
`coverage(<name>)` is always present. A row emitted only when the gate is requested would make
`componentRows` see zero rows for that component on an ungated run and report it as a dead shard,
which is wrong.

Consequence for `cmd/lydite/test.go`: the `flakyGate` type renders a row for every component in
`own` (the shard's responsibility set) via `gate.report(rep, own)`, called on both the
"nothing selected" early-return path and the normal path — not just when `gate.requested` is
true.

A second, smaller gotcha: `componentRows`'s own problem message ("`<name> has no row in any
shard's report`") does not name which label was missing, because it takes one `label func(string)
string` per call and is called once per gate kind. Folding a second gate's rows through it (as
`merge.go` now does for `flakyLabel` beside `testLabel`) produces a problem string
indistinguishable from the other gate's if both are missing — `merge.go` works around this by
prefixing its own `"flaky: "` onto that call's returned problem strings before appending them,
rather than changing `componentRows` itself (whose message text `mutation_test.go` already
asserts verbatim, outside that change's boundary).
