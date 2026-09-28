---
about: one exclude_from_mutation declaration on a line only covers the mutant(s) with the shortest replaced-text Length among those whose span contains that line, not every mutant on the line
saw:
  - source/cli/internal/mutation/sites.go
  - source/cli/internal/treesitter/testcode.go
targets: exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol
verdict: still-true
---

`sites.go:90` (`resolve`), `:94-98` (span-containment loop), `:103-106` (shortest-Length
computation) and `:107-111` (attach-to-all-of-shortest-Length loop) implement the rule.

The "shortest" compared at `sites.go:103-106` is `s.out[i].Length`, i.e. `hi-lo`, the byte
length of the text each mutant *replaces* — not the lineSpan's line range. So among mutants
whose lineSpan brackets the declared line, only the one(s) tied for smallest replaced-text
length get the reason attached at `sites.go:107-111`; every other mutant on that line is left
unmatched and will show as surviving.

Concrete shape: a `return "", false // [lydite:exclude_from_mutation][...]` line generates (at
least) two `replace-return` mutants — one replacing `""` (Length 2) and one replacing `false`
(Length 5). `resolve()` picks the shortest, `""`, and attaches the reason only to it; the
`false`->`true` mutant is never annotated. `source/cli/internal/treesitter/testcode.go` shows the
form the rule forces where both literals need declaring: lines ~323-325, ~349-350 and ~392-393
split a multi-value return across lines so each literal sits on its own line under its own
declaration. A single line-level declaration does not exclude "every mutant at that line", it
excludes only the mutant with the smallest replaced-text span.
