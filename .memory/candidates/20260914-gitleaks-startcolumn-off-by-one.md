---
about: gitleaks v8.30.1's JSON report StartColumn is one byte past a match's first byte
saw:
  - source/cli/internal/secrets/findings.go
  - source/cli/internal/secrets/testdata/gitleaks.json
---

Verified by running the pinned gitleaks (v8.30.1) over a probe tree and inspecting the
real captured report (`source/cli/internal/secrets/testdata/gitleaks.json`): a match
beginning at the very start of a line reports `StartColumn: 2`, not `1`. `prefixEnd` in
`findings.go` accounts for this by cutting the site's prefix at `col-2`, not `col-1`.

If upstream gitleaks ever changes this off-by-one, `prefixEnd`'s cut only ever shortens
toward "rule alone" (never lengthens into the matched secret), so the failure mode of a
future gitleaks version is a coarser site identity, not a credential leak.
