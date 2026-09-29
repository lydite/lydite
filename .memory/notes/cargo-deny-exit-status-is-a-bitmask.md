---
name: cargo-deny-exit-status-is-a-bitmask
kind: gotcha
description: cargo-deny's exit status is a bitmask of which of the run checks failed (licenses vs bans, e.g. 4 vs 2 vs 6), not a plain 0/1 — a verdict check must not narrow to `== 1`.
anchors:
  - path: source/cli/internal/rust/deny.go
    blob: 6ce9d156ef27
  - path: source/cli/internal/rust/testdata/README.md
    blob: 9aedc6c906cf
confidence: verified
---

Verified by running the pinned cargo-deny (0.20.2) for real, under `check licenses
bans`: a licenses-only failure exits 4, a bans-only failure exits 2, and both failing at
once exits 6 — confirmed against `deny.ndjson` (exit 4) and `deny-transitive.ndjson`
(exit 2) in `source/cli/internal/rust/testdata/`.

`denyResult` (`source/cli/internal/rust/deny.go:245`) treats any non-zero status as a
failure via `executil.Result.Ok()` (`Err == nil`, line 250), never a comparison against
a specific code. A future change that narrows the verdict check to `== 1` (the shape a
reader unfamiliar with this would reach for) would silently pass a licenses-only or
bans-only failure whose bitmask doesn't happen to equal 1.
