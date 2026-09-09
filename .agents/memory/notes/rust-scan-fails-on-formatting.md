---
name: rust-scan-fails-on-formatting
kind: gotcha
description: A Rust component's scan fails on `cargo fmt --check` over the whole crate, despite the "lydite is not a formatter" stance stated for TypeScript.
anchors:
  - path: source/cli/internal/rust/rust.go
    blob: 63c84441c3d1
  - path: AGENTS.md
    blob: 487a1761c7cb
confidence: verified
---

`rust.Check` (`internal/rust/rust.go:38`) runs four checks, and the first is
`named("cargo fmt", executil.RunEnv(ctx, dir, env.Check, "cargo", "fmt", "--check"))`
(`rust.go:40`). A failing `cargo fmt` fails the row, and so the component's whole
`lydite scan` verdict.

This reads as a contradiction against the policy stated for TypeScript in
`AGENTS.md:1250` — "lydite is not a formatter and must never report a formatting diff
as a finding" — which is why Biome's formatter and assist are disabled. `AGENTS.md:63`
compounds it: the layout table describes `source/cli/internal/rust/` as "clippy,
cargo-audit, cargo-deny", omitting fmt entirely. Only the package doc
(`rust.go:1`, "fmt, clippy, cargo-audit, cargo-deny") mentions it, and nowhere is the
inconsistency with the Biome stance explained or resolved.

Two consequences a reader will get wrong:

- It is a plain whole-tree `Check()` run, not diff-scoped. An unformatted file anywhere
  in the crate or workspace fails the component even when the change never touched it.
- There is no `rust_test.go` — only `internal/rust/pins_test.go`. Nothing asserts the
  `cargo fmt` / `cargo clippy` argv, so dropping `--check` or reordering flags has no
  regression test catching it, unlike `internal/typescript`'s `biome_test.go`.
