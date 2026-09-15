---
about: cargo's "could not compile" summary line is never in clippy's --message-format json stream
saw:
  - source/cli/internal/rust/clippy.go
  - source/cli/internal/rust/testdata/clippy-nocompile.ndjson
---

Verified by running the pinned clippy over a crate that fails to type-check
(`internal/rust/testdata/brokenprobe/`, captured as `clippy-nocompile.ndjson`): the
build-summary text a developer sees on a real terminal ("error: could not compile
`brokenprobe` (lib) due to 1 previous error") goes to cargo's own stderr and never
appears as a `compiler-message` line in the JSON stream at all.

The spanless diagnostic `clippyFindings` (`clippy.go:77-83`) drops, and `clippyNotes`
recovers as the failing row's `Detail`, is a different message: rustc's `failure-note`
level line ("For more information about this error, try `rustc --explain E0277`"). Any
future code (or comment) that describes the no-span case as "the could not compile line"
is describing text that this stream structurally cannot carry.
