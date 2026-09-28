---
name: root-scoped-gates-seed-their-own-zero-independently
kind: invariant
description: each root-scoped gate (semgrep, secrets) seeds its own zero in FindingCounts's root map independently, gated on its own cfg.<X>.Enabled — a shared seed once let a clean gitleaks run read as a gate switched off.
anchors:
  - path: source/cli/internal/stages/record/findings.go
    blob: bbc727310d74
confidence: verified
---

`FindingCounts` (`internal/stages/record/findings.go:93`, called by
`cmd/lydite/record.go`'s `findingCounts` shim and the record flow's `CountFindings`
stage) records a root-scoped gate's finding count under `root_findings`. Each
root-scoped gate seeds its own zero independently via a `seed(gate string)` closure
(`findings.go:127-133`), gated on its own `cfg.<X>.Enabled`
(`findings.go:134-139`: `if cfg.Semgrep.Enabled { seed(semgrep.Gate) }`,
`if cfg.Secrets.Enabled { seed(secrets.Gate) }`).

This was not always true: when Semgrep was the only root-scoped gate, its zero was
seeded by a single condition, a shape that silently assumed there would only ever be one
root-scoped gate to seed. Adding gitleaks as a second one exposed the assumption: a
clean gitleaks run (no findings) recorded no `gitleaks` key in `root_findings` at all,
reading identically to the gate being switched off in `.lydite/config.yml` — the exact
failure `findings.go:123-126`'s own comment now names ("a gate that could not run never
renders as one that passed... one seed shared between them would let a clean gitleaks
run read as a gate nobody asked for").

Anyone adding a third root-scoped gate must extend this same independent-seed pattern,
not assume the existing one already generalizes — it didn't, the first time it was
tested.
