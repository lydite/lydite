---
name: gosec-g101-flags-an-identifier-named-token
kind: gotcha
description: gosec's G101 matches an identifier's name, so a string constant named for a token is reported whatever its value; golangci-lint does not run gosec, so it fails only in lydite scan, which is why the scan flow's key is InputSemgrepCI.
anchors:
  - path: source/cli/internal/flows/scan/scan.go
    blob: 72c7518c93b8
  - path: source/cli/.golangci.yml
    blob: 93754b8b4e6d
  - path: source/cli/internal/golang/golang.go
    blob: 7ae7066279f1
confidence: verified
---

`internal/flows/scan/scan.go:49` declares `InputSemgrepCI = "SemgrepAppToken"`. Its doc comment gives the reason: named for the mode rather than the variable, because gosec's G101 matches the identifier, so a constant whose name says token is reported as a hardcoded credential whatever it holds. The value (the `flow.Inputs` key) and `Params.SemgrepAppToken` still say token; only the `const` identifier assigned a string literal is inspected.

The finding would come from lydite's own scan, not the lint gate: `source/cli/.golangci.yml` enables only errcheck, govet, staticcheck and unused (lines 22-27), while gosec (pinned in `internal/golang/golang.go`) runs as the `gosec(cli)` check when `lydite scan` scans the `cli` component. So a new string constant named `...Token...`, `...Secret...`, `...Password...` passes `golangci-lint` locally and then fails the scan.
