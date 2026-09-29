---
name: secret-gate-row-follows-claims-not-exit-status
kind: gotcha
description: The secret gate's row follows the claims that survive the scope filter, and a gitleaks exit of 1 with an empty report is not a pass.
anchors:
  - path: source/cli/internal/secrets/findings.go
    blob: 39e82221f384
confidence: verified
---

`verdict` in `internal/secrets/findings.go` decides the row from the claims that survive the scope filter, not from gitleaks' exit status, because gitleaks exits 1 for a leak in ignored output that no claim survives. The exit is still read, against the report, by `ranToCompletion` (`findings.go:575`): gitleaks also exits 1 for a directory it could not walk and leaves an empty report, which would otherwise read as a clean tree.

`executil.Result` carries only error or nil, so "could not run" outcomes render as a failing row with the reason in `Detail`, not the amber `StatusUnmeasured` the grammar prescribes. A walk that fails *after* finding something is indistinguishable from a finished one; the report has no errors key.
