---
about: coverage.Measure routes Python through the lcov path but skips the exclusion scan for any file with no tree-sitter grammar, because excludedLCOVLines would fail the whole measurement with ErrNoGrammar
saw:
  - source/cli/internal/coverage/exclude.go
  - source/cli/internal/coverage/coverage.go
---

`measureLCOV` asks `lcovExclusions` for each source file. It consults `treesitter.GrammarFor` first and
answers no exclusions when there are no tables, and otherwise delegates to `excludedLCOVLines`
unchanged. `excludedLCOVLines` only special-cases `ErrUnparsed`, so an `ErrNoGrammar` reaching it
would fail `Measure` on the first Python file with any coverage. The guard sits above it so Rust's
and TypeScript's contract is untouched. The consequence is that `[lydite:exclude_from_coverage]` has
no effect in a Python component.
