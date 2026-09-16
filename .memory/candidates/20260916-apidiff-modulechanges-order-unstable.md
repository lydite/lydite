---
about: apidiff.ModuleChanges iterates its packages through a Go map internally, so a report's cross-package order is not stable between runs of the same comparison
saw:
  - source/cli/internal/apisurface/apisurface.go
  - source/cli/internal/apisurface/probe_test.go
---

`apidiff.ModuleChanges` computes the diff by walking its input in a map, so calling it directly
and rendering the result in the order it comes back produces a report whose row order varies run
to run for no reason a reader could act on — confirmed empirically (task 1 of the ADR 0040
branch measured this while building the probe fixtures).

`internal/apisurface.Compare` does not call `ModuleChanges` at all: it reimplements the
base/head package matching itself, keyed on module-relative import path, walked via
`slices.Sort` over the union of both trees' package sets (`union()` in `apisurface.go`). Within
one package's comparison (`apidiff.Changes`, the per-package function — not `ModuleChanges`),
the incompatible messages are additionally sorted by text before being turned into findings.

`probe_test.go`'s own measurement test works around the same instability differently: it sorts
`module.Packages` by path before calling `apidiff.ModuleChanges`, which is enough there because
the probe only ever has one non-internal package per shape, so *within-package* ordering
(`apidiff.Changes`'s own concern) never has more than one message to order.

Anyone calling `apidiff.ModuleChanges` directly and depending on deterministic output needs to
sort on both axes — which package, and which change within it — since neither is a promise the
library makes.
