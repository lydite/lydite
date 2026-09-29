---
about: mutation's declaration-to-mutant resolver no longer selects by shortest replaced-text length — it now selects by strict span containment, so the note's description of the mechanism is out of date
saw:
  - source/cli/internal/mutation/sites.go
targets: exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol
verdict: now-false
---

The note's mutation half describes `sites.resolve` as: collect every span whose
`[first,last]` brackets the declared line, take the shortest `Length` among them, and attach
the reason to every span of that length. That is no longer what the code does.

`sites.resolve` (`source/cli/internal/mutation/sites.go`, blob `c0f53a2cc8f5` as of this
branch) now selects by containment: of the candidate spans on the declared line, it attaches
the reason to every span that no other candidate's span strictly contains (`enclosesAnother`).
The old rule used a span's byte length as a proxy for "innermost", which broke whenever two
syntactically unrelated operators of different token widths shared one line — a 2-byte
comparison (`<=`) lost to an unrelated 1-byte arithmetic operator (`+`) on the same line, though
neither nested inside the other. Containment fixes that: two spans that do not nest are now both
declared, and a span still nested inside another is still excluded, without reference to either
one's text length.

The note's broader claim — that mutation resolves to a span, never a symbol, and that this
differs structurally from coverage/CRAP's `go/ast` `FuncDecl`-attachment model — still holds.
Only the tie-break mechanism inside the span-selection step changed. The note's own worked
example (`println(a < b)`, comparison vs. whole-call deletion) still resolves the same way under
containment, since the comparison mutant's span is still the innermost one on that line.

The note's `coverage/exclude.go` anchor (`33883b842fe6`) is untouched by this branch and still
accurate at `exclude.go:73` (func signature) / `:81` (`decl.(*ast.FuncDecl)`), per the prior
recheck's line-number correction.
