---
about: the secret gate's row follows surviving claims, and gitleaks exiting 1 with an empty report is not a pass
saw:
  - source/cli/internal/secrets/findings.go
---

`verdict` in `source/cli/internal/secrets/findings.go` decides the row from the claims that
survive the scope filter, not from gitleaks' exit status, because gitleaks exits 1 for a leak
in ignored output no claim survives. The exit is still read, against the report, by
`ranToCompletion`: gitleaks exits 1 for a directory it could not walk too, and leaves an empty
report, which would otherwise read as a clean tree. `executil.Result` carries only error or
nil, so "could not run" outcomes render as a failing row with the reason in `Detail`, not the
amber `StatusUnmeasured` the grammar prescribes. A walk that fails after finding something is
indistinguishable from a finished one; the report has no errors key.
