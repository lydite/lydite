---
name: clippy-deny-warnings-skips-dependent-units
kind: gotcha
description: Under -D warnings a denied lint fails its own unit and cargo never checks what depends on it, and clippy's JSON gives no signal for the skipped targets, so a Crashed=false recording can have measured less than a clean run.
anchors:
  - path: source/cli/internal/rust/clippy.go
    blob: aefd4a4c1b6d
confidence: verified
---

`clippyResult` (`internal/rust/clippy.go`) sets `Crashed` by reading whether a failing run's `compiler-message` diagnostics are all lints (`clippyOnlyLinted`, `:221`) versus rustc's own error codes (`E<digits>`, via `rustcErrorCode`) or a codeless parse error. That tells "did not compile" from "found a lint" for the unit clippy actually reported on.

What it cannot see: cargo compiles a workspace unit by unit, and once `-D warnings` fails one unit (a lint becomes a hard error) cargo does not attempt anything depending on it — that lib's own test harness, its bins, its examples, or another workspace crate depending on it. None of those units' findings appear in the JSON stream, and there is no "skipped" record, so such a run reads "not crashed, some findings" while having measured less than a clean run would. Closing it would mean deciding clippy's verdict from its finding count instead of its exit status (dropping `-D warnings` as the verdict mechanism). ADR 0058's "A crashed scanner must not read as a clean one" section records this as an accepted, deliberately unclosed gap.
