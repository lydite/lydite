---
about: lcovExclusions's GrammarFor guard is what makes the coverage exclusion scan turn on for a language automatically the moment internal/treesitter gains its grammar, with no branch to remember to flip — Python is no longer the example of a language it skips, since it now has one
saw:
  - source/cli/internal/coverage/exclude.go
  - source/cli/internal/coverage/coverage.go
  - source/cli/internal/treesitter/treesitter.go
---

`measureLCOV` asks `lcovExclusions` for each source file. It consults `treesitter.GrammarFor` first
and answers no exclusions when there are no tables for the language, and otherwise delegates to
`excludedLCOVLines` unchanged. `excludedLCOVLines` only special-cases `ErrUnparsed`, so an
`ErrNoGrammar` reaching it would fail `Measure` on the first file of a grammarless language with
any coverage. The guard sits above it so a parsed language's contract is untouched.

Before feat/python-is-parsed-so-crap-scores-it, Python was this guard's live example: it had a
runner and an lcov report but no tree-sitter grammar, so `[lydite:exclude_from_coverage]` had no
effect in a Python component. That branch added `treesitter.Python`, so `GrammarFor(runner.Python,
...)` now answers `(Python, true)` and the exclusion scan switched on for Python by itself — the
guard's code did not change, only what it answers for Python did. Every language a runner exists
for now has a grammar; the guard is defensive for whichever future runner-language arrives without
one.
