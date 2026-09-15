---
about: a Go map small enough to fit in one bucket does not iterate as a uniform-random permutation, which makes a "randomized order" mutation-kill test unreliable at a handful of entries
saw:
  - source/cli/internal/treesitter/scope_test.go
---

`internal/treesitter/scope.go`'s `DeclaredExclusions` builds `Declared.Unused` from a range over
`map[int]annotation.Declaration`, then calls `sort.Ints(out.Unused)`. A mutation-testing run
(CI's `mutation (cli)` job) that deletes that `sort.Ints` call is meant to be caught by a test
asserting the result is in ascending order despite the source map's unspecified iteration order.

A first version of that test used six declared-unused lines and asserted exact ascending order.
It survived the sort's removal on CI's mutation gate — not once but reliably enough to need a
second fix, despite passing every local run during development. The reason: Go's map iteration
for a map small enough to live in one bucket (roughly ≤8 entries) is a random *rotation* of one
cyclic order that each key's hash fixes for the process — not a uniform-random draw from all
`n!` permutations. A six-entry map has a real (not astronomically small) chance that the
hash-seed-determined cyclic order for a given process happens to already read as ascending, so
the "prove it fails without the fix" check — which only ran a handful of times locally — didn't
surface the flake, while CI's mutation run (a fresh process, a fresh hash seed, run once per
mutant) sometimes drew the unlucky seed.

The fix: use enough entries (40, in the committed test) to force the map across multiple buckets,
where bucket-visitation order is *also* randomized, making the achievable orderings close to the
full `n!` space and the coincidence-probability negligible rather than merely unlikely. Verified
by running the reverted-fix test as 8 separate fresh processes (each `go test` invocation gets a
new hash seed) and confirming it failed every time — a local loop of a few iterations is exactly
what's needed to catch this class of flake, since it exercises multiple hash seeds the way CI's
repeated mutation runs eventually will.

General rule for any test relying on Go's map iteration to be "obviously unsorted without the
fix": count entries in the tens, not single digits, and verify the reverse-defect proof across
several process invocations, not one — a single local run share the same one-process hash seed
sampling risk CI's single mutant-run does.
