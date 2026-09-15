---
about: cargo-deny's default unused-allowed-license setting produces colliding, unlocatable findings for any generated allow-list broader than one component's dependency graph
saw:
  - source/cli/internal/rust/deny.go
  - source/cli/internal/licence/testdata/deny-licenses-unused-allowance.ndjson
---

Running the pinned cargo-deny (0.20.2) with a generated `[licenses]` table whose `allow` list is
broader than what a given crate's dependency graph actually uses (true by construction for any
org-wide policy checked against one component), the default `unused-allowed-license` setting
("warn") emits one `license-not-encountered` diagnostic per allowed-but-unused licence. Each of
these diagnostics carries an empty `graphs` array — cargo-deny has nothing to attribute the
warning to, since no crate in the graph triggered it.

`internal/rust/deny.go`'s `denySubject` (the function that turns a diagnostic's `graphs` into a
package name and version) reads an empty `graphs` as naming no crate, so each such diagnostic
locates at line 0 and shares one identical `Site` — `license-not-encountered\x1f` — with every
other one, distinguished only by ordinal. A repository-wide allow-list is by construction
broader than any single component's graph, so this fires for every generated config unless
suppressed.

The fix is `unused-allowed-license = "allow"` in the generated `[licenses]` table
(`denyLicencePolicy` in `deny.go`) — not cosmetic, it is what keeps a generated org-wide policy
from producing a handful of claims naming no package and clearable by no edit, on every Rust
component, every run.
