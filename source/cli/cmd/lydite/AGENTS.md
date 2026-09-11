# `cmd/lydite` — the commands

Each command assembles gates that live in `internal/`, so the reference that governs a change
here is usually the one named after the package it drives rather than after this file. Read it
before changing the command.

| Changing | Read |
|---|---|
| `coverage.go`, `measurements.go`, `record.go` | [`coverage.md`](../../../../.agents/references/coverage.md), and [`crap.md`](../../../../.agents/references/crap.md) for the CRAP rows |
| `test.go`, `plan.go`, `merge.go`, `fold.go` | [`components.md`](../../../../.agents/references/components.md), [`services-and-scheduling.md`](../../../../.agents/references/services-and-scheduling.md), [`shards-and-the-fold.md`](../../../../.agents/references/shards-and-the-fold.md), [`orphan-and-affected.md`](../../../../.agents/references/orphan-and-affected.md) |
| `mutation.go`, `mutation_merge.go` | [`mutation.md`](../../../../.agents/references/mutation.md) |
| `scan.go` | [`scanning.md`](../../../../.agents/references/scanning.md), [`linters.md`](../../../../.agents/references/linters.md), [`semgrep.md`](../../../../.agents/references/semgrep.md) |
| `publish.go`, `threads.go` | [`surface.md`](../../../../.agents/references/surface.md), [`findings.md`](../../../../.agents/references/findings.md) |
| `review.go`, `clearance.go`, `status.go` | [`referral-and-clearance.md`](../../../../.agents/references/referral-and-clearance.md) |
| `history.go`, the ledger rows | [`quality-history.md`](../../../../.agents/references/quality-history.md) |
| anything that adds a row or a status | [`output-grammar.md`](../../../../.agents/references/output-grammar.md) |

`var version = "dev"` is overridden at release via `-ldflags "-X main.version=<tag>"`. Keep the
variable name and the package stable.
