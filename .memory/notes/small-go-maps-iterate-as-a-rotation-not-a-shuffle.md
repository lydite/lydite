---
name: small-go-maps-iterate-as-a-rotation-not-a-shuffle
kind: gotcha
description: a Go map small enough to live in one bucket (roughly <=8 entries) iterates as a random rotation of one hash-fixed cyclic order per process, not a uniform draw over all n! permutations — a "sort removed" mutation-kill test needs tens of entries and several separate process runs, not a handful of entries checked once.
anchors:
  - path: source/cli/internal/treesitter/scope_test.go
    blob: 1ca7825caaf6
  - path: source/cli/internal/treesitter/scope.go
    blob: 98ffda9dda15
confidence: verified
---

`internal/treesitter/scope.go`'s `DeclaredExclusions` builds `Declared.Unused` from a
range over `map[int]annotation.Declaration`, then calls `sort.Ints(out.Unused)`. A
mutation-testing run (CI's `mutation (cli)` job) that deletes that `sort.Ints` call is
meant to be caught by a test asserting the result is in ascending order despite the
source map's unspecified iteration order.

A first version of that test used six declared-unused lines and asserted exact
ascending order. It survived the sort's removal on CI's mutation gate — not once but
reliably enough to need a second fix, despite passing every local run during
development. The reason: Go's map iteration for a map small enough to live in one
bucket (roughly <=8 entries) is a random *rotation* of one cyclic order that each key's
hash fixes for the process — not a uniform-random draw from all `n!` permutations. A
six-entry map has a real (not astronomically small) chance that the
hash-seed-determined cyclic order for a given process happens to already read as
ascending, so a "prove it fails without the fix" check that only ran a handful of times
locally didn't surface the flake, while CI's mutation run (a fresh process, a fresh hash
seed, run once per mutant) sometimes drew the unlucky seed.

The fix, `TestUnusedIsSortedNotIterationOrder` (`scope_test.go:245`): use 40 entries to
force the map across multiple buckets, where bucket-visitation order is *also*
randomized, making the achievable orderings close to the full `n!` space and the
coincidence-probability negligible rather than merely unlikely (test comment,
`scope_test.go:234-244`, "the only order this test accepts is one in 40!"). Verified by
running the reverted-fix test as 8 separate fresh processes (each `go test` invocation
gets a new hash seed) and confirming it failed every time.

**General rule for any test relying on Go's map iteration to be "obviously unsorted
without the fix": count entries in the tens, not single digits, and verify the
reverse-defect proof across several process invocations, not one** — a single local run
shares the same one-process hash-seed sampling risk CI's single mutant-run does.

A follow-on branch (`feat/flaky-new-tests`, ADR 0039) measured the same mechanism more
precisely: a six-entry map run across 26 fresh processes found the *cycle* itself stays
fixed across every process while only the *rotation* is resampled, and `-test.count=2`
inside one process drew the identical order twice in 3 of 6 processes measured. The
lesson this sharpens: `-count=N` cannot resample anything a process fixes once (a map
hash seed, a `sync.Once`, an `init`-seeded generator, a port, a temp-dir name) — only a
genuinely separate process invocation can. This is also why `--gate-flaky`'s own rerun
is a second `go test` process and never a `-count=2` folded into the first (ADR 0039,
"Two runs, and they are two processes").
