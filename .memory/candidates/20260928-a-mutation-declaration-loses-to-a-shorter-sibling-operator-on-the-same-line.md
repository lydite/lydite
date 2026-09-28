---
about: a [lydite:exclude_from_mutation] marker covers every mutant tied for the shortest replaced-text length on its line, so an unrelated, textually-shorter operator on the same line silently steals the declaration from a longer one it was never meant to acknowledge
saw:
  - source/cli/internal/mutation/sites.go
  - source/cli/internal/mutation/treesitter.go
  - source/cli/internal/annotation/annotation.go
  - agentic/references/mutation.md
  - docs/adr/0034-an-exclusion-declaration-is-scoped-by-a-parser-in-every-language.md
---

Root cause for: `for (let suffix = 1; suffix <= used.size + 1 && used.has(key); suffix++) { // [lydite:exclude_from_mutation][reason]`
— the trailing marker declares only the `+`→`-` mutant (arithmetic-operator, col 44) and never
the `<=`→`<` mutant (conditional-boundary, col 31), on the same line.

`annotation.Declarations` (`internal/annotation/annotation.go:140`) keys one declaration per
source line (`out[comments[i].Line] = ...`, `:159`) — a second marker embedded later in the same
comment's text is not found (`strings.CutPrefix` only matches at the very start of the comment
body, `:144`), which is why two tags packed into one trailing comment behave identically to one:
still exactly one declaration for that line.

`tsGen.emit` (`internal/mutation/treesitter.go:528-535`) sets each mutant's byte `Length` to the
replaced node's own byte span: `n.EndByte()-n.StartByte()`. `<=` is 2 bytes; `+` is 1 byte. Both
mutants are single-line (their tree-sitter node never spans a newline), so both land in
`sites.resolve`'s "inside" set for the marker's line (`sites.go:93-98`, `line >= sp.first && line
<= sp.last`) — the marker sits on the very line both operators are written on. `resolve` then
keeps only the spans tied for the *shortest* `Length` among that set (`sites.go:103-111`) and
attaches the reason to those alone. `+` (length 1) wins; `<=` (length 2) is excluded — not
because it is nested inside anything, but purely because its own token text is longer than an
unrelated sibling operator's token that happens to sit on the same source line.

This is the documented mechanism working as designed, not a bug: `agentic/references/mutation.md:86-89`
says a declaration "covers the mutants whose replaced text contains its line, and of those the
ones replacing the least" — using text length as a proxy for "innermost", modelled on one operator
with two same-width mutants (boundary shift and negation, `:94-96`: "it covers every mutant
replacing that least amount, which for one operator is both its boundary shift and its
negation"). The design's own worked example (`println(a < b)`, comparison operator vs. whole-call
deletion) is a case where the *narrower* candidate actually is the more nested one. Nothing in
`mutation.go` or ADR 0034 addresses two syntactically unrelated, differently-sized operators
(a comparison and an arithmetic op) sharing one line — there the shortest-length heuristic no
longer tracks "innermost" and instead just picks whichever operator's spelling is shorter.
`conditional-boundary` is not deliberately excluded from declaration anywhere in the codebase;
`<` / `<=` / `>` / `>=` are ordinary 1-2 byte tokens like any arithmetic operator and are
declarable whenever they are the shortest (or tied-shortest) candidate on their line.

Marker placement not on the mutant's own line (a line above the `for`, or the loop body's first
line) never matches, for a structural reason stated in `annotation.go:122-135`: a declaration is
keyed strictly to "the line its token is written on, and never [any line] its reason happens to
wrap onto" or any enclosing statement's range, because reaching further "would let a declaration
already in the tree acknowledge code a later change adds nearby" without that later change itself
carrying the marker text on a changed line — breaking the referral bargain
(`internal/referral` sees a suppression only on a line the diff touches). This is deliberate,
recorded rationale, not an oversight — it is a different design question from the
same-line shortest-length tie-break above.
