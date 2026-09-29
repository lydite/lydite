---
about: an unmatched [lydite:exclude_from_mutation] declaration (including one on a line the change did not touch) is still named on stderr, but its count now also reaches the component row and mutation.json — it is no longer diagnostic-only
saw:
  - source/cli/internal/mutation/mutation.go
  - source/cli/internal/stages/mutation/generate.go
  - source/cli/internal/stages/mutation/run.go
  - source/cli/cmd/lydite/mutation.go
---

Supersedes a prior candidate on the same subject, staged before this branch's fix. The prior
candidate's diagnosis of *why* an out-of-scope declaration is reported unmatched is unchanged
and still correct: the generators read every declaration in a scanned file, but a mutant is kept
only when every line it spans is in the diff's `lines` set, so a declaration on an untouched line
matches no kept span and is reported unmatched every time its file is otherwise in scope. Nothing
in this branch changed that scoping.

What changed is what happens to the count. `mutation.Summary` now carries an `Unmatched` field,
summed across a component's declarations and threaded from `generate`'s return through
`RunMutants`'s `Out`. `cmd/lydite/mutation.go` renders it on the row's detail as
"N declaration(s) cover no mutant", on both a passing and a failing row, and it reaches
`mutation.json` the same way `MemoryUnbounded`/`OutputHeldOpen` do. The stderr diagnostic
(`lydite: <component>: <path>:<line>: ... covers no mutant`), written as it arises per this
repo's rule that a stage's diagnostic is written to an injected writer rather than returned in
`Out`, is unchanged and still fires independently — the count on the row is additive, not a
replacement for it. Neither one affects the row's status: an unmatched declaration still cannot
fail a component on its own.
