---
about: gotreesitter's Node type exposes ChildByFieldName (single) and FieldNameForChild (per index), but no plural ChildrenByFieldName — a grammar field declared "multiple":true in its node-types.json has to be read by walking children and filtering on FieldNameForChild
saw:
  - source/cli/internal/mutation/treesitter.go
---

Verified while designing ADR 0054 (Python operator tables): checked
`github.com/odvcencio/gotreesitter@v0.52.0`'s `tree.go` for a helper that returns every child
under one field name (needed because tree-sitter-python's `comparison_operator` node declares its
`operators` field `"multiple": true`, per that grammar's own `node-types.json`). Only two relevant
methods exist — `(*Node).ChildByFieldName(name, lang)`, which returns the first match only, and
`(*Node).FieldNameForChild(i, lang)`, which names one child's field by index. There is no
`ChildrenByFieldName` or equivalent. Any future extraction of a `multiple: true` field (Python's
`comparison_operator.operators` is the first case this repository needs) has to iterate
`n.ChildCount()` and compare `n.FieldNameForChild(i, lang) == name` itself — this is a fact about
the vendored parser library, not something `internal/mutation` or `internal/treesitter` currently
works around, since neither has needed a multiple field before Python.
