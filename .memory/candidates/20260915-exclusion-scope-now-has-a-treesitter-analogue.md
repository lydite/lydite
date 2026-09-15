---
about: a function-scoped exclusion resolver now exists for Rust/TypeScript too, via tree-sitter
saw:
  - source/cli/internal/treesitter/scope.go
  - source/cli/internal/coverage/exclude.go
targets: exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol
verdict: now-false
---

Re-checked because the note is marked `stale: yes` (exclude.go moved 33883b8 -> 480d5f4).

The note's core claim was: coverage/CRAP's exclusion scope is a `go/ast.FuncDecl` and is
Go-only; the only cross-language resolver is mutation's innermost-span walk; "a symbol/
method-scoped exclusion ... does not exist for Rust or TypeScript today."

That is no longer true. `internal/coverage/exclude.go:70`'s Go-only `DeclaredExclusions`
(go/ast, `Excluded.Funcs map[*ast.FuncDecl]string`) is unchanged, but commit `5140b26
fix(coverage): an exclusion declaration takes effect in rust and typescript (#148)` added a
second, parallel `DeclaredExclusions` at `internal/treesitter/scope.go:118`:

```go
func DeclaredExclusions(lang runner.Lang, path string, src []byte, gate annotation.Gate) (Declared, error)
```

It parses with tree-sitter, resolves each declaration comment to a `Span{First, Last int}`
via `Grammar.scope` (`scope.go:160`) — the sibling-walk analogue of go/ast's doc-comment
attachment, bounded the same way (a blank line or non-continuation comment breaks the
attachment) — and returns `Declared{Funcs map[Span]string, Unused []int}`. This covers Rust
and TypeScript (see `functions` grammar table, `scope.go:24`, and `GrammarFor`).

So the function-scoped model the original note said didn't exist for Rust/TS now does, and it
is not the mutation package's span-containment resolver — it's a third, purpose-built
resolver. Anyone implementing CRAP-for-Rust/TypeScript (see
handoff/20260915-0930-crap-rust-typescript.md) should check whether `internal/crap` already
consumes `treesitter.Declared`/`treesitter.Span` for exclusions, or still only consumes
`coverage.Excluded` — reusing this resolver rather than building a fourth one is very likely
the intended path, given #148 exists specifically to plumb Rust/TS exclusions through it.
