---
about: coverage.Measure skips the lcov exclusion scan for a language treesitter.GrammarFor holds no tables for at all, because excludedLCOVLines would fail the whole measurement with ErrNoGrammar — Rust, TypeScript and Python all have tables now, so no currently-shipped runnable language exercises this in practice
saw:
  - source/cli/internal/coverage/exclude.go
  - source/cli/internal/coverage/coverage.go
targets: a-language-with-no-grammar-skips-the-lcov-exclusion-scan
verdict: corrected
---

`measureLCOV` asks `lcovExclusions` for each source file. It consults `treesitter.GrammarFor`
first and answers no exclusions when there are no tables, and otherwise delegates to
`excludedLCOVLines` unchanged. `excludedLCOVLines` only special-cases `ErrUnparsed`, so an
`ErrNoGrammar` reaching it would fail `Measure` on the first file of a grammar-less language
with any coverage. The guard sits above it so a language that does have tables is unaffected.

This candidate previously named Python as the example of a grammar-less language whose
`[lydite:exclude_from_coverage]` has no effect. That is no longer true: Python has real
tree-sitter tables as of internal/treesitter's `Python` grammar, and its exclusion scan runs
exactly as Rust's and TypeScript's does — see `source/cli/internal/coverage/coverage_test.go`'s
`TestAPythonDeclarationDeductsItsFunction`. `runner.Shell` is the one language in the shipped
set `GrammarFor` currently returns `false` for (`TestOnlyAGrammarlessLanguageSkipsTheExclusionScan`
in the same file exercises it), but Shell has no coverage runner at all, so nothing in production
actually reaches this guard today — it is exercised only synthetically, in that one test.
