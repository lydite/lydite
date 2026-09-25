---
name: clippy-deny-warnings-skips-dependent-units
kind: gotcha
about: source/cli/internal/rust/clippy.go
description: under -D warnings a denied lint fails its own compilation unit, and cargo never checks anything that depends on it — a crate's tests/bins/examples when its lib has a lint, or a dependent crate in a workspace — and clippy's own JSON report gives no signal for which targets were skipped this way.
anchors:
  - path: source/cli/internal/rust/clippy.go
    blob: aefd4a4c1b6d5a50fb405c6e9dd8dab6b8af9480
confidence: verified
---

`clippyResult` (`clippy.go`) sets a `Crashed` verdict (added this session) by reading
whether a failing run's `compiler-message` diagnostics are all lints
(`clippyOnlyLinted`) versus rustc's own error codes (`E<digits>`, via
`rustcErrorCode`) or a codeless parse error. That correctly tells "did not compile" from
"found a lint" for the unit clippy actually reported on.

What it cannot see: cargo compiles a workspace unit by unit, and once `-D warnings`
fails one unit (turns a lint into a hard error), cargo does not attempt anything that
depends on it — a lib's own test harness, its bins, its examples, or another crate in
the workspace that depends on it. None of those units' clippy findings ever appear in
the JSON stream at all; there is no "skipped" record to detect, distinguishing this from
an ordinary clean pass over a smaller unit set is not possible from clippy's own output
today. A recording taken from such a run reads as "not crashed, some findings" while
actually having measured less than a full clean run would have. Closing this needs
deciding clippy's pass/fail verdict from its finding count rather than its exit status
(dropping `-D warnings` as the verdict mechanism), which is a change to what "clippy
failed" means — see ADR 0058's "A crashed scanner must not read as a clean one" section
for where this was accepted as a known, narrower, deliberately-unclosed gap.
