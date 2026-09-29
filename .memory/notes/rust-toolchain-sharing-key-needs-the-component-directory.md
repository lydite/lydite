---
name: rust-toolchain-sharing-key-needs-the-component-directory
kind: gotcha
description: toolchain.Ensure's shared-resolution cache is keyed by lang+version+raw for every language except Rust, where two components declaring the identical requirement (or declaring nothing) can still resolve to different toolchains because rustup selects per directory.
anchors:
  - path: source/cli/internal/toolchain/toolchain.go
    blob: 30dbfa828d12
confidence: verified
---

`Ensure` (`toolchain.go`) shares one `resolution` across every component asking for the same
`req.Lang + req.Version + req.Raw`, so two components pinning the same Go version cost one probe.
That sharing is wrong for Rust: rustup resolves a toolchain by walking up from the *working
directory* a `cargo`/`rustup` invocation runs in, looking for `rust-toolchain.toml`/an override —
so two components declaring the identical thing (or nothing, both unpinned) can still be pinned to
different channels by files sitting in their own directories.

The fix appends `req.Unit.Dir` to the sharing key only when `req.Lang == runner.Rust` (confirmed at
`toolchain.go:270-271`, and reused at lines ~419, ~484, ~533). Any future change to how requirements
are grouped or cached in this package has to preserve that: Rust is the one language here where
"same declared requirement" is not "same resolved toolchain."
</content>
