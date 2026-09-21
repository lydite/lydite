---
about: Go coverage producer fingerprint vs args:-declared -coverpkg narrowing
saw: source/cli/internal/runner/runner.go (Runner.Producer, ~line 1082)
---

`Runner.Producer` for `go-test` returns only the Go toolchain version — it does not fold in
the component's declared `args:`, and therefore not a declared `-coverpkg` scope narrowing
(see `[[gotest-coverpkg-last-flag-wins-over-component-args]]`). So today, if a component
narrows its coverage denominator by declaring `-coverpkg=./internal/...` in `args:`, the
producer string used for baseline comparison does not change even though the measured
quantity did, and the shift reads as a real coverage regression or improvement rather than an
incomparable-producer case.

This is the same class of problem ADR 0025 closes for a toolchain/dependency version bump
(e.g. Vitest 3.2.7 → 4.1.11 changing coverable lines from 345 to 152 over an identical tree),
just triggered by a component author instead of a dependency bump, and it is **not yet
closed** for this case. Tracked as lydite/lydite#207 (open as of 2026-09-21); the fix shape is
explicitly undecided in the issue (whether *any* `args:` change should fingerprint, or only
flags known to move the denominator; whether the fingerprint is a literal string or a hash).
