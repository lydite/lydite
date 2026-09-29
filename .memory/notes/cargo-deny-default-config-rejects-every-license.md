---
name: cargo-deny-default-config-rejects-every-license
kind: gotcha
description: cargo-deny's default config (no deny.toml present) rejects every license, MIT included, so a crate with no deny.toml fails `check licenses` regardless of how it is actually licensed.
anchors:
  - path: source/cli/internal/rust/deny.go
    blob: 6ce9d156ef27
  - path: source/cli/internal/rust/testdata/deny-clean.ndjson
    blob: bd43f55414a9
confidence: verified
---

Verified by running the pinned cargo-deny (0.20.2) over a crate with no `deny.toml`: it
logs `unable to find a config path, falling back to default config` as a `type:"log"`
line on the JSON stream, and that default config rejects every license — so an
unconfigured crate cannot pass `check licenses` no matter its actual license.

`deny-clean.ndjson` (`source/cli/internal/rust/testdata/`), the fixture standing in for
a passing run, was only capturable by giving the probe crate its own `deny.toml`
allowing MIT. Anyone adding a new cargo-deny fixture and expecting a "clean" crate to
pass with no config present will get a failing run instead — the cause is this default,
not a fixture bug. See also [[cargo-deny-unused-allowed-license-warning-collides]] for
what generating that config for real then triggers.
