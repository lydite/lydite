---
name: typescript-optional-chaining-splits-across-two-node-shapes
kind: gotcha
description: "TypeScript's tree-sitter grammar represents `?.` differently on a member access (a named optional_chain wrapper node) than on a call (a bare unnamed token on call_expression) — a complexity counter must handle both shapes or it double- or under-counts."
anchors:
  - path: source/cli/internal/crap/treesitter.go
    blob: a8d9f43c7f2e
confidence: verified
---

`a?.b` and `f?.()` both write optional chaining, but the grammar does not give them one
node type. `a?.b` wraps the access in a named `optional_chain` node under
`member_expression`. `f?.()` has no such wrapper — its `?.` is a bare, unnamed child
token written directly onto the `call_expression` node itself.

`complexityTypeScript` (`treesitter.go:225`) counts these separately for this reason:
`optional_chain` is counted directly (`treesitter.go:237`), and `call_expression` is
inspected via `optionalCall` (`treesitter.go:264`), which scans the call's direct
children for a literal `?.` token. Counting both node types unconditionally would
double-count `a?.b()`, since ESLint (the oracle backing this rule, see ADR 0036) counts
the optional chain exactly once regardless of whether it ends in a call.

Anyone touching TypeScript/TSX tree-sitter node types for a new construct should verify
the actual grammar shape empirically (dump a probe fixture's tree) rather than assume
one node type per syntactic form — this is the second place in this codebase (after ADR
0034's lcov `FN` record findings) where the obvious 1:1 mapping between syntax and tree
shape does not hold.
