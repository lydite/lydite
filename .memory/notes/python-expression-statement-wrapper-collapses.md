---
name: python-expression-statement-wrapper-collapses
kind: gotcha
description: gotreesitter's Python parser collapses an expression_statement with one named child into that child, so a removable-statement rule keyed on the wrapper never fires; statementParents handles it.
anchors:
  - path: source/cli/internal/mutation/treesitter.go
    blob: f2e95e3b3a19
confidence: verified
---

Found reviewing `lydite mutation`'s Python support (ADR 0054): the first `pythonGrammar` named `expression_statement` as `statement` and listed `call`, `assignment` and `augmented_assignment` under `removable`, mirroring Rust/TypeScript. It generated **zero** `remove-statement` mutants. Parsing `if True:\n    log(total)\n    counter += 1\n` through `treesitter.Python` and reading the tree with `(*Node).SExpr` shows `log(total)` and `counter += 1` directly under `block`, with no `expression_statement` anywhere, so `visit`'s `case typ == t.g.statement:` never matches.

This is gotreesitter's own Python parser collapsing the wrapper: `expression_statement` survives only when it wraps more than one named child (a semicolon-separated line). Rust's and TypeScript's always survives, so this only matters for Python. The fix: `grammar` gained `statementParents []string` (`internal/mutation/treesitter.go:142`; `{"module", "block"}` for Python, nil for Rust/TypeScript/TSX) and `visit` a second path to `RemoveStatement` — a node whose type is in `removable` and whose `Parent()` type is in `statementParents` is emitted directly, its own byte range being the whole statement. A future language whose statement wrapper can disappear the same way should use this pattern.
