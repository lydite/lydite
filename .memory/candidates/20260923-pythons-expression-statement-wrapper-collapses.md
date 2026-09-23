---
about: gotreesitter's Python parser collapses an expression_statement holding exactly one named child into that child, so an ordinary call/assignment/augmented_assignment is a direct child of module/block rather than wrapped — a removable-statement rule keyed only on the statement wrapper's node type never fires for Python
saw:
  - source/cli/internal/mutation/treesitter.go
---

Found reviewing `lydite mutation`'s Python support (ADR 0054's implementation): the first cut of
`pythonGrammar` named `expression_statement` as `statement` and listed `call`, `assignment` and
`augmented_assignment` under `removable`, mirroring Rust/TypeScript exactly. It generated zero
`remove-statement` mutants — confirmed by parsing `if True:\n    log(total)\n    counter += 1\n`
through `treesitter.Python` and reading the tree with `(*Node).SExpr`: `log(total)` and
`counter += 1` appear directly under `block`, with no `expression_statement` node anywhere, so
`visit`'s `case typ == t.g.statement:` never matches.

This is gotreesitter's own Python parser doing the collapsing (not a lydite bug in the sense of
misreading a correct tree) — `expression_statement` survives in the tree only when it wraps more
than one named child (rare in practice; Python's grammar allows a semicolon-separated statement
list on one line). Rust's and TypeScript's `expression_statement` always survives, so this only
matters for Python.

The fix: `grammar` gained a `statementParents []string` field (`{"module", "block"}` for Python,
nil for Rust/TypeScript/TSX) and `visit` gained a second path to `RemoveStatement` — a node whose
own type is in `removable` and whose `Parent()` type is in `statementParents` is emitted directly
(there is no wrapper left to delete; the removable node's own byte range is the whole statement).
Any future language whose statement wrapper can similarly disappear should look for this pattern
rather than assume `statement`'s single-string match is sufficient — the fix generalizes past
Python if another language's tree-sitter grammar ever does the same collapsing.
