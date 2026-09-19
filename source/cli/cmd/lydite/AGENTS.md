# `cmd/lydite` — the commands

Each command assembles gates that live in `internal/`, so the reference that governs a change
here is usually the one named after the package it drives rather than after this file. Read it
before changing the command.

| Changing | Read |
|---|---|
| `coverage.go`, `measurements.go`, `record.go` | [`coverage.md`](../../../../agentic/references/coverage.md), and [`crap.md`](../../../../agentic/references/crap.md) for the CRAP rows |
| `test.go`, `plan.go`, `merge.go`, `fold.go` | [`components.md`](../../../../agentic/references/components.md), [`services-and-scheduling.md`](../../../../agentic/references/services-and-scheduling.md), [`shards-and-the-fold.md`](../../../../agentic/references/shards-and-the-fold.md), [`orphan-and-affected.md`](../../../../agentic/references/orphan-and-affected.md) |
| `mutation.go`, `mutation_merge.go` | [`mutation.md`](../../../../agentic/references/mutation.md) |
| `scan.go` | [`scanning.md`](../../../../agentic/references/scanning.md), [`linters.md`](../../../../agentic/references/linters.md), [`semgrep.md`](../../../../agentic/references/semgrep.md) |
| `publish.go`, `threads.go` | [`surface.md`](../../../../agentic/references/surface.md), [`findings.md`](../../../../agentic/references/findings.md) |
| `review.go`, `review_apisurface.go`, `clearance.go`, `status.go` | [`referral-and-clearance.md`](../../../../agentic/references/referral-and-clearance.md) |
| `history.go`, the ledger rows | [`quality-history.md`](../../../../agentic/references/quality-history.md) |
| anything that adds a row or a status | [`output-grammar.md`](../../../../agentic/references/output-grammar.md) |

`var version = "dev"` is overridden at release via `-ldflags "-X main.version=<tag>"`. Keep the
variable name and the package stable.
