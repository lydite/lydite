---
about: cargo fmt still gates but never reports for Rust scans; the note's "no rust_test.go" detail is now out of date
saw:
  - source/cli/internal/rust/rust.go
  - source/cli/internal/rust/rust_test.go
  - source/cli/cmd/lydite/scan_test.go
  - agentic/references/scanning.md
  - agentic/references/linters.md
targets: rust-scan-fails-on-formatting
verdict: still-true
---

Re-checked because the note was stale on all four anchors (two "missing" because the referenced
paths moved from `.agents/references/*` to `agentic/references/*`, not because the content is
gone; two file-hash drift).

Core claim holds: `rust.Check` (now `rust.go:69`, was `:62`) still runs
`named(GateFmt, executil.RunEnv(ctx, dir, env.Check, "cargo", "fmt", "--check"))` as its own row
(`rust.go:70`), and `FindingGates()` (`rust.go:60`, was `:53`) still excludes `GateFmt` on
purpose ("GateFmt is not among them, and must not be", `rust.go:56-58`). `scan_test.go:1333`
still asserts `!slices.Contains(scannerGates(runner.Rust), rust.GateFmt)`.
`agentic/references/scanning.md:207` and `agentic/references/linters.md:104` still state the
"lydite is not a formatter" policy that `GateFmt`'s row contradicts.

One correction: the note said `source/cli/internal/rust/` "now has audit/clippy/deny/
lockfile/ndjson/pins tests, but no `rust_test.go`". `source/cli/internal/rust/rust_test.go` now
exists (145 lines: `TestFindingsDetailIndentsEachClaimsOwnDetailBeneathIt`,
`TestFindingsDetailIsEmptyWithoutClaims`, `TestUnreadableNamesTheGateAndTheExitStatus`,
`TestOneRunPerGateKeepsEveryCapturedClaimsFingerprint`). It still does not assert the `cargo fmt`
or `cargo clippy` command line, so the substance of the gotcha (no regression test would catch a
dropped `--check` or reordered flag) still holds — only the "no rust_test.go at all" phrasing is
now false.
