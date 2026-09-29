---
name: flaky-classname-read-from-run1-report-not-source-layout
kind: rationale
description: A Rust or TypeScript test's Classname (nextest binary name, vitest file path) is deliberately never computed from source layout in internal/flaky — it is read back out of run 1's own JUnit report by matching declared bare names against classnamesOf(opts.Run1, name), because reconstructing cargo's or vitest's naming rules in Go would be fragile.
anchors:
  - path: source/cli/internal/flaky/rerun.go
    blob: c2aa92f96301
confidence: verified
---

`internal/treesitter.DeclaredTests` gives a bare qualified name only — it has no way to know which
nextest binary a `tests/*.rs` file compiles into (needs the crate's package name plus nextest's own
target-naming rules) or what path string vitest will report as `classname` (depends on cwd and
vitest's own path-relativization).

`expand` (`rerun.go`) sidesteps reimplementing either tool's naming rules: it takes a bare declared
name and scans `opts.Run1`'s already-composite-keyed outcome map for every classname that name
actually appears under, via `classnamesOf` (matches on the `junit.ClassKey("", name)` suffix). A
name matching zero classnames in run 1's report is `Unmeasured` ("run 1's report does not record
it"), never guessed at. A name matching two or more classnames becomes two or more independent
`Test` units, deduplicated by `(Scope, Classname, Name)` and anchored to a source declaration via
`anchor` (exact file-path match for vitest; first-declaration fallback for Rust, since nothing ties
a nextest binary to one file). Mirrors the philosophy `Unmeasured`'s doc comment states for Go:
"decided by absence from the report rather than by a parser guessing."
</content>
