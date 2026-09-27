---
about: gosec's G101 (hardcoded credentials) matches on the identifier's name, so a string constant whose name contains "Token" is reported whatever its value — which is why the scan flow's input key is InputSemgrepCI, not InputSemgrepAppToken
saw:
  - source/cli/internal/flows/scan/scan.go
  - source/cli/internal/golang/golang.go
  - source/cli/.golangci.yml
---

`internal/flows/scan/scan.go` declares `InputSemgrepCI = "SemgrepAppToken"`. The constant's doc
comment gives the reason for the name: "Named for that mode rather than for the variable: gosec's
G101 matches the identifier, so a constant whose name says token is reported as a hardcoded
credential whatever it holds." The value (the `flow.Inputs` key) and the `Params.SemgrepAppToken`
field still say token; only the `const` identifier assigned a string literal is what G101 inspects.

The finding would come from lydite's own scan, not from the lint gate: `.golangci.yml` enables
only errcheck, govet, staticcheck and unused, while gosec (pinned in
`internal/golang/golang.go`) runs as the `gosec(cli)` check when `lydite scan` scans the `cli`
component. So a new string constant named `...Token...`, `...Secret...`, `...Password...` and so on
passes `golangci-lint` locally and then fails the scan.
