---
name: clippy-nocompile-line-never-reaches-the-json-stream
kind: gotcha
description: cargo's "error, could not compile ... due to N previous errors" summary line never appears in clippy's --message-format json stream — it goes only to cargo's own stderr.
anchors:
  - path: source/cli/internal/rust/clippy.go
    blob: aefd4a4c1b6d
  - path: source/cli/internal/rust/testdata/clippy-nocompile.ndjson
    blob: 80c8c3d2a141
confidence: verified
---

Verified by running the pinned clippy over a crate that fails to type-check
(`internal/rust/testdata/brokenprobe/`, captured as `clippy-nocompile.ndjson`): the
build-summary text a developer sees on a real terminal ("error: could not compile
`brokenprobe` (lib) due to 1 previous error") goes to cargo's own stderr and never
appears as a `compiler-message` line in the JSON stream at all.

The spanless diagnostic `clippyFindings` (`clippy.go:64`) drops, and `clippyNotes`
(`clippy.go:259`) recovers as the failing row's `Detail`, is a *different* message:
rustc's `failure-note` level line ("For more information about this error, try `rustc
--explain E0277`"). Any future code (or comment) that describes the no-span case as "the
could not compile line" is describing text this stream structurally cannot carry.
