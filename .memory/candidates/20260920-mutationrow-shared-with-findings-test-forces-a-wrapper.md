---
name: mutationrow-shared-with-findings-test-forces-a-wrapper
kind: gotcha
about: source/cli/cmd/lydite/mutation.go
description: mutationRow is called from both mutateComponent (production) and findings_test.go (several call sites), so a change to what a completed row is worth to the exit code cannot be threaded into mutationRow's own signature without touching findings_test.go too.
anchors:
  - path: source/cli/cmd/lydite/mutation.go
    blob: 02b5149b468c00483a1f030a1e4aca19419891d3
  - path: source/cli/cmd/lydite/findings_test.go
    blob: a08b817fad23803a5239328148c2ed130b6032c3
confidence: verified
---

`mutationRow(label, name, dir, log, summary, results, scoped, elapsed)` builds a component's
row from its mutation results and is the one place a survivor decides `StatusFail` vs
`StatusPass`. It is called from `mutateComponent`'s production path, but it is also called
directly from `findings_test.go` (several call sites) to build rows for finding-rendering
tests that have nothing to do with gating.

`--no-gate` (ADR 0048) needed a way to turn a completed row's `StatusPass`/`StatusFail` into
`StatusContext` without gating anything, and `mutationRow` itself was not the place to put
that logic in this session: widening its signature to take a `gate`/`noGate` parameter would
have required editing every `findings_test.go` call site too. Instead the conversion lives in
a separate function, `completedRow(row ui.Row, noGate bool) ui.Row`, applied once at
`mutateComponent`'s own return — `mutationRow`'s output and its test call sites are
untouched.

Anyone adding another axis that changes what a completed row is worth (gating, publishing,
anything else) should check whether `mutationRow`'s signature is really the right place, or
whether a wrapper at the production call site avoids widening a function `findings_test.go`
also calls.
