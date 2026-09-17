---
name: nextest-tool-config-file-needs-tool-name-prefix
kind: gotcha
about: source/cli/internal/runner/runner.go
description: cargo-nextest's --tool-config-file flag requires a tool-name-prefixed value (e.g. "lydite:/path/to/config.toml") — a bare path is a hard, immediate error, not a silently-ignored config.
anchors:
  - path: source/cli/internal/runner/runner.go
    blob: 3fda35250102822d2a806f8d3c66208b71d3e63e
confidence: verified
---

`askNextestJUnit` (`runner.go`) appends `--tool-config-file`, `"lydite:"+cfg`, never a
bare `cfg`. Confirmed against a real `cargo nextest run` invocation during ADR 0041's
capture work: a bare path fails outright rather than being treated as an unnamed/default
tool config. Anything that builds a nextest invocation asking for the staged tool config
(`internal/runner/pins.go`'s `nextestToolConfig()`) must keep the `lydite:` prefix — it
is nextest's own namespacing for a tool-authored config layer, not a lydite convention
that could be dropped without consequence.
