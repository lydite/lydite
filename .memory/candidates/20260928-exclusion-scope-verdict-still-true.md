---
about: re-verified the funcdecl-vs-mutant-span exclusion-scope invariant; still true, two line numbers drifted
saw:
  - source/cli/internal/coverage/exclude.go
  - source/cli/internal/mutation/sites.go
targets: exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol
verdict: still-true
---

Re-checked because the note is marked `stale: yes` (`coverage/exclude.go` blob moved,
`33883b8 -> f0a1ada`).

The claim holds. `DeclaredExclusions` walks `file.Decls` and matches `*ast.FuncDecl` doc
comments — but at `exclude.go:73` (func signature) and `:81` (`fn, ok := decl.(*ast.FuncDecl)`),
not `:70`/`:77-89` as the note says. `sites.resolve` (`internal/mutation/sites.go:90`) is
unchanged: containment loop `:93-98`, shortest-length selection `:103-106`, attach loop
`:107-111` (note said `:94-98`, off by one). Both mechanisms are exactly as described: Go
coverage/CRAP resolves to a whole function via `go/ast` attachment; mutation resolves to the
mutant span(s) tied for shortest `Length` (byte length of the replaced text) among every span
whose `[first,last]` contains the declaration's own line — never a symbol, never a
language-independent scope.
