---
name: rust-scan-fails-on-formatting
kind: gotcha
description: A Rust component's scan still fails on whole-crate `cargo fmt --check`, contradicting the "lydite is not a formatter" stance — a known open question, not an oversight to fix in passing.
anchors:
  - path: source/cli/internal/rust/rust.go
    blob: 94ec217c45f9
  - path: .agents/references/scanning.md
    blob: 6391d6dc78ab
  - path: .agents/references/linters.md
    blob: bc2e79c5a199
  - path: AGENTS.md
    blob: 561198d6d584
confidence: verified
---

`rust.Check` (`internal/rust/rust.go:62`) runs four checks, and the first is
`named(GateFmt, executil.RunEnv(ctx, dir, env.Check, "cargo", "fmt", "--check"))`
(`rust.go:67`). `named` (`rust.go:92`) makes it a row of its own, so a failing
`cargo fmt` fails the row and with it the component's whole `lydite scan` verdict.

This contradicts the policy stated for TypeScript in `.agents/references/linters.md:93`
— "lydite is not a formatter and must never report a formatting diff as a finding" —
which is why Biome's formatter and assist are disabled. **The contradiction is known and
deliberately unresolved**, so do not treat it as a bug to fix in passing:

- `.agents/references/scanning.md:124-126`: "`cargo fmt` gets no parser... That its row
  still fails a Rust component contradicts that, and is a separate open question."
- `docs/adr/0032-every-scanner-reports-its-findings-as-data.md:193-195` says the same,
  "not settled here".

The resolution so far is a split: fmt **gates** but never **reports**. `FindingGates()`
(`rust.go:53`) returns clippy/audit/deny only, and its doc (`rust.go:49-52`) says
`GateFmt` "is not among them, and must not be". `scan_test.go:995` holds that —
it asserts `rust.GateFmt` is absent from the Rust scanner's gates.

Two things a reader will still get wrong:

- It is a plain whole-tree `Check()` run, not diff-scoped. An unformatted file anywhere
  in the crate or workspace fails the component even when the change never touched it.
- The **argv** is untested. `source/cli/internal/rust/` now has audit/clippy/deny/
  lockfile/ndjson/pins tests, but no `rust_test.go`, and nothing asserts the
  `cargo fmt` / `cargo clippy` command line — dropping `--check` or reordering flags has
  no regression test catching it, unlike `internal/typescript`'s `biome_test.go`.
- `AGENTS.md:121`'s layout table still describes `source/cli/internal/rust/` as "clippy,
  cargo-audit, cargo-deny", omitting fmt. Only the package doc (`rust.go:1`) lists it.
