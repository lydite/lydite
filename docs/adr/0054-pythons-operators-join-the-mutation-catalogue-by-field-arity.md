# Python's operators join the mutation catalogue by field arity, not by a walk of their own

[#221](https://github.com/lydite/lydite/issues/221) asks the question `internal/mutation/treesitter.go`
warns about in its own comment: a language needing its own traversal is evidence the catalogue has
stopped being one taxonomy. Python's infix expressions split across three node types —
`binary_operator`, `comparison_operator`, `boolean_operator` — where Rust and TypeScript both have
one. Read against tree-sitter-python's own `node-types.json` rather than against how that split first
looked from the outside, it does not need a walk. It needs the catalogue's one assumption —
`grammar.binary` names a node type with a single `operator` **field** — relaxed to a node type, a
field name, and whether that field holds one token or many.

`boolean_operator` turns out to need nothing new at all: it carries a singular `operator` field, is
shaped exactly like `binary_operator`, and nests for a chain (`a and b or c` is two nodes, confirmed by
`internal/crap/treesitter.go`'s own comment on the already-merged Python grammar) rather than packing
several operators into one. `comparison_operator` is the one true irregular: `a < b < c` is a single
node whose `operators` field is declared `multiple: true` — still a named field, just one
`gotreesitter.Node.ChildByFieldName` cannot return whole, because that call only ever returns the
first match. Getting the rest means walking children and comparing `FieldNameForChild` against the
name, which is more code than a single lookup but not a different *kind* of extraction — it is the
same field-based lookup, generalized to its own declared arity.

## `binary` becomes a small table of node type, field name, and arity

```go
type binaryKind struct {
    nodeType string
    field    string
    multiple bool
}

binary []binaryKind
```

Rust and TypeScript each get a one-element slice: `{binary_expression, operator, single}`. Nothing
about their generated mutants, byte ranges, or golden fixtures changes — `emit` already only ever
receives a byte range, and never knew which node type or field produced it. Python gets three entries:
`{binary_operator, operator, single}`, `{boolean_operator, operator, single}`,
`{comparison_operator, operators, multiple}`. The `visit` switch changes from a single string
comparison to a lookup across the slice, and the extraction method changes from "read one field" to
"read one field, of declared arity, and return every token it names" — one method serving both
arities, since a `multiple: false` field is the one-element case of the same walk. `sites.go`'s
`emit`/`resolve` pair, `internal/referral`, and `internal/finding` reach mutation only through byte
ranges and the `Operator` enum; none of them know a node type exists, so none of them change.

## A chained comparison is one mutant per operator token, not one per node

Each token the field extraction returns generates its own mutant, at its own byte range, exactly as
`binary_operator`'s single token does today. `a < b < c` therefore produces two independent boundary-
shift/negate candidates rather than one mutant covering an ambiguous span — a mutant's identity is the
operator token, never the node that happened to carry it.

This sharpens a limitation the catalogue already has rather than introducing a new one.
`sites.go`'s exclusion resolver matches a `[lydite:exclude_from_mutation]` declaration by line, then
attaches it to whichever mutant(s) tie for the shortest replaced text among every mutant whose span
covers that line. For a single operator today, its boundary shift and its negation are the same
token length and always tie, so one declaration already silently covers both — accepted, because both
mutants are about the *same* comparison. In a chain, the two `<` tokens are the same length too, so a
declaration on that line ties across *both* comparisons, not just the one the author meant to
acknowledge. Retuning the tie-break to key on token position instead of shortest length would fix this
and the single-operator case together, but it is a change to `sites.go` that predates Python and is
not scoped into this decision — Python only makes an existing gap visible sooner. An implementer
should state it as a caveat in the golden fixture's comments, not attempt to close it.

## `and`/`or` ships as a fourth operator table; conjunct removal does not

`boolean_operator`'s `operator` field takes exactly `and` or `or`, so swapping one for the other is a
fourth table beside `boundaryOps`, `negateOps`, and `arithmeticOps` — no new mechanism, the same
extraction Q1 already generalized. A second candidate mutant — deleting a conjunct, `a and b` → `a` —
is not shipped. Python's short-circuit semantics mean a test that does not distinguish `a`'s truth
value from `a and b`'s kills nothing there whether or not the tests are any good, which is a
structurally higher equivalent-mutant rate than a token swap ever has, dominated by the operator's own
semantics rather than by a gap in coverage. It is a real mutant kind in the abstract; it is not one
whose typical instance is worth generating today. Naming it here rather than silently dropping it is
what tells the implementer this was considered and deferred, not missed.

## The build-only floor gets a caveat in prose, not a new status

Python's build-only variant is `pytest --collect-only -q`; Python's late name binding means a mutant
that breaks a call still collects, so a mutant that a compiled language's build-only step would reject
as unviable instead runs, and if nothing exercises the broken path, reads as killed by construction.
[#214](https://github.com/lydite/lydite/issues/214) recorded this already as "a limitation of the
language, not a defect." It stays exactly that: a documented caveat, not a new outcome status.
`arithmeticOps`'s own doc comment already accepts an equivalent-shaped imprecision — a `+` swap on a
string either fails to compile (Rust, caught as unviable) or silently produces `NaN` (TypeScript,
"the accepted cost of a tree without types") — with no mechanical detection and no distinct row.
Python's collect-only floor is the same kind of accepted cost: the score means "close to what
compiling would have measured," not "identical to it," and that goes in the golden fixture's
comments and the row's documentation rather than in a new `internal/mutation` outcome, because there
is nothing to detect — a name error that never executes leaves no trace to detect it by.

## Worth building, and the ordering #221 already stated still holds

None of the above accumulated into an exception — each question resolved into a small, bounded
addition to the existing shape rather than a reason to bend it or a reason to stop. Python mutation is
worth building on that basis.

[#221](https://github.com/lydite/lydite/issues/221)'s ordering — the generator lands before the
`mutation.Tree`/`backendFor` case, because a backend with no generator pays for a full instrumented
baseline, compose stack, `setup`, and deferred teardown per component per run to report a row that
was always going to be unmeasured — is untouched by anything decided here: every change above is
generator-side (`internal/mutation/treesitter.go`, `sites.go`), and none of it changes what running
the backend first would cost. It still holds, unconditionally.

What has changed since #221 was filed is that its own prerequisite is now met:
[#220](https://github.com/lydite/lydite/issues/220) (the Python tree-sitter grammar `mutation.grammarFor`
composes through `treesitter.GrammarFor`) merged as
[#227](https://github.com/lydite/lydite/pull/227), ahead of this decision. The implementing handoff
does not need to gate on that grammar landing — it can build directly on top of it.

## Consequences

- `internal/mutation/treesitter.go`: `grammar.binary` becomes `[]binaryKind` (node type, field name,
  arity); the `visit` switch and the extraction method change shape; `rustGrammar`/`tsGrammar` each
  get a one-element `binary` slice with identical behaviour and byte-identical golden fixtures.
- A new `pythonGrammar` entry: three `binaryKind` rows (`binary_operator`, `boolean_operator` single;
  `comparison_operator` multiple), plus `returns`, `statement`, `removable`, `literals`, and `comment`
  entries sized the way #220's CRAP grammar work already sized them for Python's syntax.
- A fourth operator table, `booleanOps` (`and` ↔ `or`), beside `boundaryOps`/`negateOps`/`arithmeticOps`.
- No change to `sites.go`, `internal/referral`, `internal/finding`, or the outcome vocabulary in
  `agentic/references/mutation.md` — the tie-break amplification and the build-only caveat are both
  documented, not mechanically closed.
- New golden fixtures under `internal/mutation/testdata/` for Python, including a chained-comparison
  case and a case documenting the exclusion tie-break's amplified scope.
- The implementing handoff carries the ordering forward unchanged: the generator and its golden
  fixture land first; the `backendFor` case lands only once the generator exists.
