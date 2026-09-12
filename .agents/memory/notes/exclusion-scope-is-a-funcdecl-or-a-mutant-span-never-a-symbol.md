---
name: exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol
kind: invariant
description: The two exclusion-scope resolvers are go/ast FuncDecl attachment (coverage/CRAP, Go only) and innermost-containing mutant span (mutation, all languages) — neither resolves a symbol.
anchors:
  - path: source/cli/internal/coverage/exclude.go
    blob: 33883b842fe6
  - path: source/cli/internal/mutation/sites.go
    blob: aa193f9bd4ea
confidence: verified
---

An `[lydite:exclude_from_<gate>]` declaration is resolved to a scope by one of two
mechanisms, and they are not the same shape. Anyone designing a symbol- or
method-scoped exclusion is choosing between them, not extending one.

- **Coverage/CRAP** — `DeclaredExclusions` (`internal/coverage/exclude.go:70`) walks
  `file.Decls`, and for each `*ast.FuncDecl` with a doc comment checks whether any line
  of that doc group carries a declaration (`exclude.go:77-89`). The scope is therefore
  **the whole function the comment is attached to**, and the attachment is `go/ast`'s
  own. That makes it Go-only, by the language rather than by choice — see
  `.agents/references/crap.md:41`, "Go alone, and that is a property of the language".
- **Mutation** — `sites.resolve` (`internal/mutation/sites.go:90`) works across all
  three languages via tree-sitter, but resolves to **the innermost mutant span
  containing the declaration's line**: it collects every span whose `first..last`
  brackets the line (`sites.go:94-98`), takes the shortest `Length` among them
  (`sites.go:103-106`) and attaches the reason to every span of that length
  (`sites.go:107-111`). A declaration next to
  `println(a < b)` picks the comparison mutant over the whole-call-deletion mutant by
  span containment, not by symbol.

Consequence: a symbol/method-scoped exclusion is closest to the coverage/CRAP model, but
that model does not exist for Rust or TypeScript today. Mutation's tree-sitter walk
resolves spans, never declarations, and would need new per-grammar logic to find "the
enclosing function/impl/method". See
[[no-lydite-annotation-excludes-a-scanner-finding]] for why that grammar is closed in
the first place.
