---
about: the cmd/lydite test package takes roughly 6-10 minutes under -race, close enough to go test's default 10-minute panic timeout that a bare `go test` there can die with a timeout panic and no failing test
saw:
  - source/cli/cmd/lydite/mutation_test.go
  - source/cli/AGENTS.md
---

Running `go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript
grammar_subset_tsx grammar_subset_python' ./cmd/lydite/...` measured 353s-588s wall-clock across
several runs on one machine, and one run tripped go test's default 10m limit: a timeout panic with
no `--- FAIL` line anywhere. The suite drives real end-to-end runs (mutation, fold, record), so the
time is the work rather than one slow test.

Verification commands run against this package should pass `-timeout 30m`. A timeout panic with
no failing test is the package being slow, not a regression to bisect.
