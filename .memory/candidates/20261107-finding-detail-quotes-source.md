---
about: "a finding's Detail is source text, not tool prose: gosec's is the quoted code around the flagged line, so anything published beyond the scan report (the lydite branch, the open-findings snapshot) must drop Detail as well as Site"
saw:
  - source/cli/internal/finding/finding.go
  - source/cli/internal/golang/gosec.go
  - source/cli/internal/ledger/open.go
  - source/cli/internal/stages/ledger/compose.go
  - docs/adr/0081-record-writes-the-open-findings-snapshot.md
---
`finding.Finding.Detail` (finding.go:99-102) is documented as "a call stack, a code excerpt, the lines a rule
matched". gosec fills it with `excerpt(issue.Code)` (gosec.go:108), the source lines including a G101 hard-coded
credential; clippy's is the rendered diagnostic, which quotes source too. Dropping only `Site` therefore does not
keep source out of a published file. `ledger.OpenFinding` (open.go) has no Detail field and compose.go's
`openFindings` copies only path/line/end_line/rule/severity/message/ordinal, for this reason (ADR 0081).
A test in stages/ledger/write_test.go asserts a secret in either Site or Detail never reaches the written snapshot.
