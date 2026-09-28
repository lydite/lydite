---
name: gitleaks-startcolumn-is-one-byte-past-the-match
kind: gotcha
description: gitleaks v8.30.1's JSON report StartColumn is one byte past a match's first byte, not the first byte itself — a site's prefix cut has to subtract 2, not 1.
anchors:
  - path: source/cli/internal/secrets/findings.go
    blob: 39e82221f384
  - path: source/cli/internal/secrets/testdata/gitleaks.json
    blob: 2feae78dc5c1
confidence: verified
---

Confirmed against the real captured report from the pinned gitleaks
(`source/cli/internal/secrets/testdata/gitleaks.json:7`): a match beginning at the very
start of a line reports `StartColumn: 2`, not `1`. `prefixEnd`
(`source/cli/internal/secrets/findings.go:519-524`) accounts for this by cutting the
site's prefix at `col-2`, not `col-1` — documented in the function's own comment
(`findings.go:507-513`).

If a future gitleaks version changes this off-by-one, `prefixEnd`'s cut only ever
shortens toward "rule alone" (`max(col-2, 0)`, never lengthens into the matched secret),
so the failure mode is a coarser site identity, not a credential leak.
