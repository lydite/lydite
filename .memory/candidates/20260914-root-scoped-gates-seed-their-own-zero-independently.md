---
about: recordstages.FindingCounts seeds each root-scoped gate's zero independently, gated on its own cfg.<X>.Enabled; a single shared seed once let a clean gitleaks run read as a gate switched off
saw:
  - source/cli/internal/stages/record/findings.go
  - source/cli/cmd/lydite/record.go
---

`FindingCounts` (`internal/stages/record/findings.go`; `cmd/lydite/record.go`'s `findingCounts`
is a thin shim over it, and the record flow's `CountFindings` stage calls it too) records a
root-scoped gate's finding count under `root_findings`. When Semgrep was the only root-scoped
gate, its zero was seeded with a single `if cfg.Semgrep.Enabled { root =
map[string]int{semgrep.Gate: 0} }` — a shape that silently assumed there would only ever be one
root-scoped gate to seed.

Adding gitleaks as a second root-scoped gate (`internal/secrets`) exposed the assumption:
a clean gitleaks run (no findings) recorded no `gitleaks` key in `root_findings` at all,
which reads identically to the gate being switched off in `.lydite/config.yml` — the
exact failure this repo's own rule ("a gate that could not run never renders as one that
passed") forbids. Each root-scoped gate now seeds its own zero independently,
gated on its own `cfg.<X>.Enabled`, via a small `seed(gate string)` closure inside
`FindingCounts` — mirroring how the per-component loop already seeds every gate
`scannerGates` (a `recordstages.ScannerGates` function the CLI hands in from
`cmd/lydite/scan.go`) names for the component's language, individually.

Anyone adding a third root-scoped gate must extend this same seeding, not assume the
existing pattern already generalizes — it didn't, the first time it was tested.
