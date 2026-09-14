---
about: findingCounts seeded only semgrep.Gate's zero, silently, until a second root-scoped gate needed its own
saw:
  - source/cli/cmd/lydite/record.go
---

`findingCounts` (`record.go`, inside the `findingCounts` function) records a root-scoped
gate's finding count under `root_findings`. Before this branch, the only root-scoped
gate was Semgrep, and its zero was seeded with a single `if cfg.Semgrep.Enabled { root =
map[string]int{semgrep.Gate: 0} }` — a shape that silently assumed there would only ever
be one root-scoped gate to seed.

Adding gitleaks as a second root-scoped gate (`internal/secrets`) exposed the assumption:
a clean gitleaks run (no findings) recorded no `gitleaks` key in `root_findings` at all,
which reads identically to the gate being switched off in `.lydite/config.yml` — the
exact failure this repo's own rule ("a gate that could not run never renders as one that
passed") forbids. The fix makes each root-scoped gate seed its own zero independently,
gated on its own `cfg.<X>.Enabled`, via a small `seed(gate string)` closure — mirroring
how the per-component `scannerGates` loop already seeds every applicable gate
individually.

Anyone adding a third root-scoped gate must extend this same seeding, not assume the
existing pattern already generalizes — it didn't, the first time it was tested.
