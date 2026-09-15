---
about: a crate with no deny.toml fails cargo-deny's licenses check even when properly licensed
saw:
  - source/cli/internal/rust/deny.go
  - source/cli/internal/rust/testdata/deny-clean.ndjson
---

Verified by running the pinned cargo-deny (0.20.2) over a crate with no `deny.toml`: it
logs `unable to find a config path, falling back to default config` as a `type:"log"`
line on the JSON stream, and that default config rejects every license, MIT included —
so an unconfigured crate cannot pass `check licenses` regardless of how it is licensed.

`deny-clean.ndjson` (`internal/rust/testdata/`), the fixture standing in for a passing
run, was only capturable by giving the probe crate its own `deny.toml` allowing MIT.
Anyone adding a new cargo-deny fixture and expecting a "clean" crate to pass with no
config present will get a failing run instead, and the reason is this default rather
than a fixture bug.
