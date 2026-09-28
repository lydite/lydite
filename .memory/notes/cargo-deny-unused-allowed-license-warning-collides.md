---
name: cargo-deny-unused-allowed-license-warning-collides
kind: gotcha
description: cargo-deny's default unused-allowed-license="warn" produces colliding, unlocatable findings (empty graphs, shared Site) for any generated allow-list broader than one component's own dependency graph — which a repo-wide policy always is.
anchors:
  - path: source/cli/internal/rust/deny.go
    blob: 6ce9d156ef27
  - path: source/cli/internal/licence/testdata/deny-licenses-unused-allowance.ndjson
    blob: 778258ea0e88
confidence: verified
---

Running the pinned cargo-deny (0.20.2) with a generated `[licenses]` table whose `allow`
list is broader than what a given crate's dependency graph actually uses (true by
construction for any org-wide policy checked against one component), the default
`unused-allowed-license` setting ("warn") emits one `license-not-encountered`
diagnostic per allowed-but-unused license. Each carries an empty `graphs` array — cargo-
deny has nothing to attribute the warning to, since no crate in the graph triggered it.

`denySubject` (`source/cli/internal/rust/deny.go:125`) reads an empty `graphs` as naming
no crate, so each such diagnostic locates at line 0 and shares one identical `Site` —
`license-not-encountered\x1f` — with every other one, distinguished only by ordinal.

The fix, applied in `denyLicencePolicy` (`deny.go:326-345`), is generating
`unused-allowed-license = "allow"` into the `[licenses]` table — not cosmetic (see the
function's own comment, `deny.go:329-334`): it's what keeps a generated org-wide policy
from producing claims naming no package and clearable by no edit, on every Rust
component, every run. See also [[cargo-deny-default-config-rejects-every-license]].
