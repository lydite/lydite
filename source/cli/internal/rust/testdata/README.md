# Captured reports from the pinned Rust tools

Every report here is the real output of a pinned tool over a real crate, captured verbatim:
`cargo clippy --all-targets --message-format json -- -D warnings` (stdout), `cargo-audit audit
--json` (stdout), `cargo-deny deny --format json check licenses bans` (**stderr** — cargo-deny
writes nothing to stdout under `--format json`).

A report alone cannot say whether the gate passed. `--json` over a crate whose only advisories
are warnings and `--json` over a crate with none both exit 0, while a crate that fails to
compile exits non-zero with a report whose only extra diagnostic carries no span and locates
nothing. So a fixture that stands in for a run carries the status the tool exited with: each
`<name>` fixture has a sibling `<name>.exit` holding that integer.

cargo-deny's status is a bitmask of which checks failed — licenses 4, bans 2, and 6 for both —
so a verdict read off it tests for non-zero, never for 1.

A crate a report names is committed beside it as a probe directory, each file suffixed `.txt`
and materialised through `internal/fixture`; see that package for why the suffix exists.

| Fixture | Exit | What it captures |
|---|---|---|
| `clippy.ndjson` | 101 | two lints, each emitted once per target (`clippyprobe`) |
| `clippy-clean.ndjson` | 0 | a crate with no lints: artefacts and `build-finished`, no diagnostic |
| `clippy-nocompile.ndjson` | 101 | a crate that fails to type-check — one located error, and the spanless `For more information about this error` note, which locates nothing (`brokenprobe`) |
| `audit.json` | 1 | two advisories in `vulnerabilities.list` (`auditprobe`) |
| `audit-clean.json` | 0 | a crate with no dependencies and no advisories |
| `audit-warnings.json` | 0 | an unmaintained advisory in `warnings` and nothing in `vulnerabilities.list`: cargo-audit does not fail on it (`warnprobe`) |
| `deny.ndjson` | 4 | an unlicensed crate: two warnings and an error (`auditprobe`) |
| `deny-transitive.ndjson` | 2 | a banned crate reached through a dependency path, from a `check bans` run — under lydite's own `check licenses bans` the same crate also fails licenses and the status is 6 (`auditprobe`) |
| `deny-clean.ndjson` | 0 | a licensed crate under a config that allows its licences: the summary line alone |
