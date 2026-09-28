---
about: config.Default() enables both Semgrep and Secrets, so recordstages.FindingCounts always pre-seeds its root map under a default config; a test of the nil-root-map (create-on-demand) path must switch both off, or it passes without exercising that branch
saw:
  - source/cli/internal/config/config.go
  - source/cli/internal/stages/record/findings.go
  - source/cli/internal/stages/record/findings_test.go
---

`config.Default()` (`internal/config/config.go`) sets `Semgrep: Semgrep{Enabled: true, Config:
"auto"}` and `Secrets: Secrets{Enabled: true}`. `FindingCounts`
(`internal/stages/record/findings.go`) starts `var root map[string]int` as nil and calls its
`seed` closure for `semgrep.Gate` when `cfg.Semgrep.Enabled` and for `secrets.Gate` when
`cfg.Secrets.Enabled`, so under a default config `root` is already a non-nil map before the
findings loop runs. The loop's own `if root == nil { root = map[string]int{} }` — which exists so
a root-scoped claim arriving for a gate the configuration switched off is recorded instead of
panicking on a nil-map write, in the one job holding a push token — is only reached when both
gates are off.

So a test meant to cover that branch has to disable both, as
`TestARootScopedClaimIsRecordedWithItsGateSwitchedOff` (`findings_test.go`) does
(`cfg.Semgrep.Enabled = false; cfg.Secrets.Enabled = false`, with a comment saying gitleaks is off
"so nothing has seeded the map before the claim arrives"). Disabling only Semgrep leaves the
Secrets seed creating the map, and the test passes while the branch it names goes unexecuted — so a
mutant that deletes that `nil` check survives it. The same applies to any test asserting
`len(root) == 0` (e.g. `TestAComponentWithNoApplicableGateRecordsNoCount`): both root-scoped gates
must be off, or their seeded zeros are in `root`. A third root-scoped gate added to
`config.Default()` enabled would have to be switched off in these tests too.
