---
name: rust-toolchain-sharing-key-needs-the-component-directory
kind: gotcha
about: source/cli/internal/toolchain/toolchain.go
description: Ensure's shared-resolution cache is keyed by lang+version+raw for every language except Rust, where two components declaring the identical requirement (or declaring nothing) can still resolve to different toolchains because rustup selects per directory.
anchors:
  - path: source/cli/internal/toolchain/toolchain.go
    blob: d8a33c0c1d0882c11d34d83144f3edac69bc4b37
confidence: verified
---

`Ensure` (`toolchain.go`) shares one `resolution` across every component asking for the
same `req.Lang + req.Version + req.Raw`, so two components pinning the same Go version
cost one probe and one diagnostic line. That sharing is wrong for Rust: rustup resolves
a toolchain by walking up from the *working directory* a `cargo`/`rustup` invocation
runs in, looking for `rust-toolchain.toml`/an override — so two components that declare
the identical thing (or declare nothing, both unpinned) can still be pinned to different
channels by files sitting in their own directories.

The fix (in the same `Ensure` function) appends `req.Unit.Dir` to the sharing key only
when `req.Lang == runner.Rust`. Any future change to how requirements are grouped or
cached in this package has to preserve that: Rust is the one language here where "same
declared requirement" is not "same resolved toolchain."
