---
name: config-default-preseeds-findingcounts-root-map
kind: gotcha
description: config.Default() enables both Semgrep and Secrets, so FindingCounts pre-seeds its root map; a test of the nil-root-map branch must switch both off or it passes without reaching it.
anchors:
  - path: source/cli/internal/config/config.go
    blob: 8b25a1b7bdf7
  - path: source/cli/internal/stages/record/findings.go
    blob: bbc727310d74
  - path: source/cli/internal/stages/record/findings_test.go
    blob: 4b91f6ac0904
confidence: verified
---

`config.Default()` (`internal/config/config.go:254-255`) sets `Semgrep{Enabled: true, Config: "auto"}` and `Secrets{Enabled: true}`. `FindingCounts` (`internal/stages/record/findings.go`) starts `var root map[string]int` nil and calls its `seed` closure (`:128`) for the semgrep gate when `cfg.Semgrep.Enabled` and for the secrets gate when `cfg.Secrets.Enabled`, so under a default config `root` is already non-nil before the findings loop. The loop's own `if root == nil { root = map[string]int{} }` — which exists so a root-scoped claim for a gate the configuration switched off is recorded instead of panicking on a nil-map write, in the one job holding a push token — is reached only when both are off.

A test covering that branch must disable both, as `TestARootScopedClaimIsRecordedWithItsGateSwitchedOff` (`findings_test.go:157`) does. Disabling only Semgrep leaves the Secrets seed creating the map, the test passes, and a mutant deleting the `nil` check survives. Any test asserting `len(root) == 0` needs both off too, and a third root-scoped gate enabled in `config.Default()` would have to be switched off in these tests as well.
