---
about: only internal/mutation/treesitter.go's grammar.binary/visit/binary() assume one infix node type with a single-valued field named "operator"; nothing downstream (sites.go, referral, finding, golden fixtures) cares how many node types, fields, or mutants-per-node a language produces
saw:
  - source/cli/internal/mutation/treesitter.go
  - source/cli/internal/mutation/sites.go
  - source/cli/internal/mutation/golang.go
---

Read for ADR 0054 (Python operator-table design), re-verifying issue #221's design question.

`grammar.binary` (treesitter.go:87) is a single string compared in `visit`'s switch
(`case typ == t.g.binary:`, treesitter.go:289) and dispatched to `(*tsGen).binary`
(treesitter.go:306), which does `n.ChildByFieldName("operator", t.lang)` — a field lookup, not a
token scan.

Corrected against tree-sitter-python's own `node-types.json`: Python's `boolean_operator` has a
normal singular `operator` field, structurally identical to `binary_expression` (and, per
`internal/crap/treesitter.go`'s comment on the merged Python CRAP grammar, `a and b or c` is two
separate `boolean_operator` nodes, not one node with two operators — it nests rather than
chaining). `comparison_operator` does carry a field too — `operators` — it is simply declared
`"multiple": true`, so `a < b < c` is one node whose `operators` field holds two tokens. What
actually breaks is narrower than "no field": it is that `ChildByFieldName` only ever returns the
first match for a field name, so a `multiple: true` field needs a different extraction call (see
`gotreesitter-has-no-childrenbyfieldname` candidate/note) — this specific function, not the
surrounding architecture, is what needs generalizing.

Downstream is already node-count-agnostic: `emit` (treesitter.go:374) takes one already-resolved
node `n` and reads its own `StartPoint`/`EndPoint`/byte range — it has no notion of "the" binary
node for a line, so a walk that emits once per operator token under one parent needs no change to
`emit`, `sites.add`, or `Mutant`. `sites.go`'s line-scoped, shortest-span exclusion resolver
(`resolve`, sites.go:90-111) groups by line and by replaced-text length, not by node type or by
"one binary expression per line" — a chained comparison producing two adjacent operator mutants on
one line is exactly the shape the shortest-span tie-break already handles for other operators (see
`mutation-declaration-picks-shortest-replaced-text-not-all-mutants` candidate/note), though it also
means a single exclusion comment on that line now ties across both comparisons in the chain, not
just one — an amplification of that existing tie-break, not a new mechanism.

`grep -rn "mutation\." source/cli/internal/referral source/cli/internal/finding` found nothing —
neither package reaches into `internal/mutation`'s node types; both consume only `Operator`
(a string constant) and aggregate counts. The golden fixtures (`internal/mutation/testdata/`) are
per-language files with their own `.golden.json`; adding a Python fixture pair is additive, not a
schema change.

So the blast radius of `binary` becoming per-node-type (a list of node type, field name, and
arity) is contained to `treesitter.go`'s `grammar` struct, `visit`'s switch, and a new extraction
function replacing `(*tsGen).binary`'s single `ChildByFieldName` call — nothing else in the
codebase encodes "exactly one binary node type, one field value, per language" as an assumption to
unwind.
