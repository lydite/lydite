---
about: cargo-deny's exit status is a bitmask of which checks failed, not a plain 0/1
saw:
  - source/cli/internal/rust/deny.go
  - source/cli/internal/rust/testdata/README.md
---

Verified by running the pinned cargo-deny (0.20.2) for real, under `check licenses bans`:
a licenses-only failure exits 4, a bans-only failure exits 2, and both failing at once
exits 6 — confirmed against `deny.ndjson` (exit 4) and `deny-transitive.ndjson` (exit 2)
in `internal/rust/testdata/`.

`denyResult` (`deny.go`) treats any non-zero status as a failure via `executil.Result.Ok()`
(`Err == nil`), never a comparison against a specific code. A future change that narrows
the verdict check to `== 1` (the shape a reader unfamiliar with this would reach for)
would silently pass a licenses-only or bans-only failure.
