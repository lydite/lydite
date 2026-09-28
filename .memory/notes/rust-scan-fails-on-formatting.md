---
name: rust-scan-fails-on-formatting
kind: gotcha
description: A Rust component's scan still fails on whole-crate `cargo fmt --check`, contradicting the "lydite is not a formatter" stance — a known open question, not an oversight to fix in passing.
anchors:
  - path: source/cli/internal/rust/rust.go
    blob: bf00a7329c1e
  - path: agentic/references/scanning.md
    blob: 828707d272af
  - path: agentic/references/linters.md
    blob: baa7f8e3ce63
  - path: agentic/references/layout.md
    blob: 2cf82b3632b7
  - path: source/cli/internal/rust/rust_test.go
    blob: f968acc157d7
  - path: source/cli/cmd/lydite/scan_test.go
    blob: 18a6374d6628
confidence: verified
---

`rust.Check` (`internal/rust/rust.go:69`) runs four checks, and the first is
`named(GateFmt, executil.RunEnv(ctx, dir, env.Check, "cargo", "fmt", "--check"))`
(`rust.go:74`). `named` makes it a row of its own, so a failing
`cargo fmt` fails the row and with it the component's whole `lydite scan` verdict.

This contradicts the policy stated for TypeScript in `agentic/references/linters.md:103-104`
— "lydite is not a formatter and must never report a formatting diff as a finding" —
which is why Biome's formatter and assist are disabled. **The contradiction is known and
deliberately unresolved**, so do not treat it as a bug to fix in passing:

- `agentic/references/scanning.md:207-209`: "`cargo fmt` gets no parser... That its row
  still fails a Rust component contradicts that, and is a separate open question."
- `docs/adr/0032-every-scanner-reports-its-findings-as-data.md:217` says the same,
  "not settled here".

The resolution so far is a split: fmt **gates** but never **reports**. `FindingGates()`
(`rust.go:60`) returns `GateClippy, GateAudit, GateDeny, GateLicence` — `GateFmt` is not
among them — and its doc (`rust.go:56-59`) says `GateFmt` "is not among them, and must
not be". `scan_test.go:857` holds that: it asserts `rust.GateFmt` is absent from the
Rust scanner's gates.

Two things a reader still gets wrong:

- It is a plain whole-tree `Check()` run, not diff-scoped. An unformatted file anywhere
  in the crate or workspace fails the component even when the change never touched it.
- The **argv** is still untested even though `source/cli/internal/rust/rust_test.go`
  now exists (it did not when this note was first written) — its tests cover findings
  formatting (`TestFindingsDetailIndentsEachClaimsOwnDetailBeneathIt`,
  `TestOneRunPerGateKeepsEveryCapturedClaimsFingerprint`, etc.), not the `cargo fmt` /
  `cargo clippy` command line. Dropping `--check` or reordering flags has no regression
  test catching it, unlike `internal/typescript`'s `biome_test.go`.
- `agentic/references/layout.md:63`'s layout table still describes
  `source/cli/internal/rust/` as "clippy, cargo-audit, cargo-deny", omitting fmt (this
  table lived in the root `AGENTS.md` before the toolkit source-tree move in commit
  b3d23a2). Only the package doc (`rust.go:1`) lists fmt.
