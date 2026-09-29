---
name: exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol
kind: invariant
description: Three exclusion-scope resolvers exist — go/ast FuncDecl (coverage/CRAP, Go), a tree-sitter analogue (coverage/CRAP, Rust/TypeScript), and innermost-containing mutant span (mutation, all languages, picks only the shortest-replaced-text mutant on a line) — none resolves a symbol.
anchors:
  - path: source/cli/internal/coverage/exclude.go
    blob: f0a1ada4cf28
  - path: source/cli/internal/mutation/sites.go
    blob: aa193f9bd4ea
  - path: source/cli/internal/treesitter/scope.go
    blob: 98ffda9dda15
  - path: source/cli/internal/treesitter/testcode.go
    blob: 23ccdf01e3cc
confidence: verified
---

An `[lydite:exclude_from_<gate>]` declaration is resolved to a scope by one of **three**
mechanisms today (originally two — see below), and they are not the same shape.

- **Coverage/CRAP, Go** — `DeclaredExclusions` (`internal/coverage/exclude.go:73`) walks
  `file.Decls`, and for each `*ast.FuncDecl` with a doc comment checks whether any line
  of that doc group carries a declaration. The scope is **the whole function the
  comment is attached to**, via `go/ast`'s own doc-comment attachment.
- **Coverage/CRAP, Rust and TypeScript** — a second, parallel `DeclaredExclusions`
  (`internal/treesitter/scope.go:173`) now exists, added in commit `5140b26` ("an
  exclusion declaration takes effect in rust and typescript", #148). It parses with
  tree-sitter and resolves each declaration comment to a `Span{First, Last int}` via
  `Grammar.scope` (`scope.go:215`) — the sibling-walk analogue of go/ast's doc-comment
  attachment (a blank line or non-continuation comment breaks it the same way) —
  returning `Declared{Funcs map[Span]string, Unused []int}` (`scope.go:129-136`). This
  is a **third, purpose-built resolver**, not a reuse of mutation's span-containment
  walk below; it covers whatever `GrammarFor` names (Rust and TypeScript at minimum).
  So the function-scoped model is no longer Go-only, and a Rust/TypeScript
  symbol/method-scoped exclusion is now possible the same way Go's already was.
- **Mutation, all three languages** — `(*sites).resolve` (`internal/mutation/sites.go:90`)
  resolves to **the innermost mutant span containing the declaration's line**: it
  collects every span whose `first..last` brackets the line (`sites.go:93-98`), takes
  the shortest `Length` among them (`sites.go:103-106`) and attaches the reason only to
  the span(s) tied for that shortest length (`sites.go:107-111`) — every other mutant on
  the line is left unmatched and surfaces as a survivor. Concretely, a
  `return "", false // [lydite:exclude_from_mutation]...` line generates a
  Length-2 (`""`) and a Length-5 (`false`) mutant; `resolve` attaches the reason only to
  the shorter one. `internal/treesitter/testcode.go:324-325,349-350,392-393` shows the
  form this rule forces when both literals need excluding: split across two lines so
  each has its own declaration.

Anyone implementing CRAP for Rust/TypeScript should reuse `treesitter.Declared`/
`treesitter.Span` (it exists specifically to plumb this) rather than building a fourth
resolver, and check whether `internal/crap` already consumes it or still only consumes
`coverage.Excluded`. See [[no-lydite-annotation-excludes-a-scanner-finding]] for why the
declaration grammar itself is closed to non-lydite-native gates in the first place.
