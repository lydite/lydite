---
name: classname-comes-from-run1s-report-never-source-layout
kind: rationale
about: source/cli/internal/flaky/rerun.go
description: a Rust or TypeScript test's Classname (nextest binary name, vitest file path) is deliberately never computed from source layout in internal/flaky — it is read back out of run 1's own JUnit report by matching declared bare names against classnamesOf(opts.Run1, name), because reconstructing cargo's or vitest's naming rules in Go would be fragile against workspace crates, [[test]] name overrides, and vitest config nobody here sees.
anchors:
  - path: source/cli/internal/flaky/rerun.go
    blob: c2aa92f96301b496e97c2570b9e7a03c9975a439
confidence: verified
---

`internal/treesitter.DeclaredTests` gives a bare qualified name only (e.g.
`tests::nested::doubles_deeper`, `outer > inner > holds a title`) — it has no way to know
which nextest binary a `tests/*.rs` file compiles into (that requires the crate's package
name from `Cargo.toml` plus nextest's own target-naming rules) or what path string vitest
will report as `classname` (depends on cwd and vitest's own path-relativization).

`expand` (`rerun.go`) sidesteps reimplementing either tool's naming rules: it takes a bare
declared name and scans `opts.Run1`'s already-composite-keyed outcome map
(`junit.ReadOutcomesByClass`) for every classname that name actually appears under via
`classnamesOf`, which matches on the `junit.ClassKey("", name)` suffix. A name matching
zero classnames in run 1's report is `Unmeasured` ("run 1's report does not record it"),
never guessed at. A name matching two or more classnames becomes two or more independent
`Test` units (the `shared_name` in `nextestprobe::a` vs `nextestprobe::b` collision ADR
0041 documents) — deduplicated by `(Scope, Classname, Name)` and anchored to a source
declaration via `anchor` (exact file-path match for vitest; first-declaration fallback for
Rust, since nothing ties a nextest binary to one file). This is the same philosophy
`Unmeasured`'s doc comment already states for Go: "decided by absence from the report
rather than by a parser guessing... the report is what actually ran."

Any future code that needs a Rust/TypeScript test's classname should reuse this
report-derived approach rather than adding Cargo.toml/vitest-config parsing.
