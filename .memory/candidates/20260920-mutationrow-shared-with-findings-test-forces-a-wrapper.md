---
name: mutationrow-shared-with-findings-test-forces-a-wrapper
kind: gotcha
about: source/cli/cmd/lydite/mutation.go
description: mutationRow is called from kindRow's KindCompleted arm (production) and directly from findings_test.go and mutation_test.go (many call sites), so a change to what a completed row is worth to the exit code is applied by a wrapper at the production call site rather than threaded into mutationRow's own signature.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 42d44e167d073f970c70ff265aca2516e1bda35f
  - path: source/cli/cmd/lydite/findings_test.go
    blob: 6092bd520db14fe6cfd45de29f1aa8378f83d185
confidence: verified
---

`mutationRow(label, name, dir, log, summary, results, scoped, elapsed)` (`cmd/lydite/mutation.go`
line ~709) builds a component's row from its mutation results and is the one place a survivor
decides `StatusFail` vs `StatusPass`. Its production caller is `kindRow`'s `KindCompleted` arm
(line ~597), which turns a `mutationstages.ComponentOutcome` into a row. It is also called
directly from `findings_test.go` (seven call sites, lines ~114-332) to build rows for
finding-rendering tests that have nothing to do with gating, and from `mutation_test.go` (a dozen
more).

`--no-gate` (ADR 0048) turns a completed row's `StatusPass`/`StatusFail` into `StatusContext`
without gating anything, and that conversion is not in `mutationRow`: widening its signature to
take a `noGate` parameter would mean editing every test call site too. It lives in a separate
function, `completedRow(row ui.Row, noGate bool) ui.Row` (line ~672), applied once in `kindRow`'s
`KindCompleted` arm — `mutationRow`'s output and its test call sites are untouched.

Anyone adding another axis that changes what a completed row is worth (gating, publishing,
anything else) should check whether `mutationRow`'s signature is really the right place, or
whether a wrapper in `kindRow` avoids widening a function the tests call directly.
