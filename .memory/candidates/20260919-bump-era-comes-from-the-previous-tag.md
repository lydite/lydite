---
name: bump-era-comes-from-the-previous-tag
kind: invariant
about: source/cli/cmd/lydite/release.go
description: bumpAdmitsBreak decides whether a break is admitted using the previous tag's version era (0.x vs 1.x+), not the target tag's — a release crossing the 1.0.0 boundary (v0.2.0 to v1.0.0) still admits a break even though it is measured on the minor, which goes from 2 to 0.
anchors:
  - path: source/cli/cmd/lydite/release.go
    blob: 0be59318699a2aff15d9aeb91ce1d9ba78effa49
confidence: verified
---

`bumpAdmitsBreak(previous, tag)` finds "the leftmost non-zero version component" (ADR
0010, ADR 0045) by inspecting `previous`, not `tag`: while `previous`'s major is `0`, the
component that must increase is the minor (or the patch, if `previous` is itself `0.0.x`);
once `previous`'s major is non-zero, only the major admits a break.

Taking the era from the target tag instead is very nearly an equivalent mutant — it only
differs on a release that crosses the `1.0.0` boundary. `v0.2.0 → v1.0.0` must admit a
break (ADR 0010's example), and the comparison is done over the *prefix through* the
previous tag's leftmost non-zero component (`semver.Major`/`semver.MajorMinor` on
`previous`, compared against the same prefix of `tag`), which is what makes that boundary
case answer correctly. `go test`'s mutation gate may report a survivor here if the
"previous vs target" choice is ever swapped without also checking this case.
