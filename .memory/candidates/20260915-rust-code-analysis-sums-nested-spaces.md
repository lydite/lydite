---
about: rust-code-analysis-cli's per-function cyclomatic JSON is not the per-function number
saw:
  - source/cli/internal/crap/testdata/README.md
---

`rust-code-analysis-cli --metrics --output-format json` nests a `spaces` array per enclosing
scope, and a function's own `metrics.cyclomatic.sum` **includes every nested space's value** —
a closure or a nested `fn` inside it is counted twice if read from `sum`. A function with one
nested space reads its own contribution from `min`/`max` instead: a probe function `outer`
containing a nested `fn inner` (each complexity 2) reports `sum 4, min 2, max 2`, and `tally`
containing a closure (own complexity 1, closure complexity 2) reports `sum 3, min 1, max 2`.

This mattered when checking lydite's own hand-rolled Rust complexity walk (ADR 0036,
`internal/crap/treesitter.go`) against the oracle: reading `sum` would have manufactured a
divergence that reading `min`/`max` does not show. Anyone re-running this oracle — to check a new
counting-rule decision, or to re-verify after a `rust-code-analysis-cli` upgrade — needs to read
the same fields, not `sum`.
