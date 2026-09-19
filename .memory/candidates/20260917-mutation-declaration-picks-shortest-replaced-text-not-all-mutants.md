---
about: one exclude_from_mutation declaration on a line only covers the mutant(s) with the shortest replaced-text Length among those whose span contains that line, not every mutant on the line
saw:
  - source/cli/internal/mutation/sites.go
  - source/cli/internal/treesitter/testcode.go
targets: exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol
verdict: still-true
---

Re-verified because the note was marked stale (its coverage/exclude.go anchor had moved).
The mutation-package portion of the note is unaffected: `sites.go:90` (`resolve`),
`:94-98` (span-containment loop), `:103-106` (shortest-Length computation),
`:107-111` (attach-to-all-of-shortest-Length loop) match the current file exactly, same
line numbers.

Extra precision the note doesn't spell out, relevant to a concrete case: the "shortest"
compared at `sites.go:103-106` is `s.out[i].Length`, i.e. `hi-lo`, the byte length of the
text each mutant *replaces* — not the lineSpan's line range. So among mutants whose
lineSpan brackets the declared line, only the one(s) tied for smallest replaced-text
length get the reason attached at `sites.go:107-111`; every other mutant on that line
is left unmatched and will show as surviving.

Concrete repro: `source/cli/internal/treesitter/testcode.go:310` has
`return notATest, "", false // [lydite:exclude_from_mutation][...]`. This generates (at
least) two `replace-return` mutants on that line: one replacing `""` (Length 2) and one
replacing `false` (Length 5). `resolve()` picks the shortest, `""`, and attaches the
reason only to it — the `false`->`true` mutant (Length 5) is never annotated and keeps
surviving. To cover both, the annotation grammar as implemented requires either two
separate declarations (one per literal/column) or an equivalence expressed differently;
a single line-level declaration does not exclude "every mutant at that line", it excludes
only the mutant with the smallest replaced-text span.
