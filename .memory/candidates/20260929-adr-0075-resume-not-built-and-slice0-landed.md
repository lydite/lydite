---
about: ADR 0075 (mutation resume/deadline) has a plan but no code yet; the plan's slice 0 (-timeout kept out of go build) has already landed, so two stale notes are now-false
saw:
  - docs/adr/0075-a-mutation-run-resumes-and-stops-at-a-deadline.md
  - agentic/plans/pr22-mutation-resumes.md
  - source/cli/internal/runner/runner.go
  - .lydite/components.yml
  - source/cli/internal/mutation/executor.go
  - source/cli/internal/stages/mutation/run.go
---

Checked 2026-09-29.

- `grep -rniE "deadline|fresh|Fingerprint|Incomplete"` over `cmd/lydite/mutation.go`, `internal/mutation`, `internal/stages/mutation`, `internal/flows/mutation` finds nothing except a comment in `executor.go:97`. No state, fingerprint, `--deadline`, `--fresh`, `KindIncomplete` exists. Only plan: `agentic/plans/pr22-mutation-resumes.md` (4 slices; slice 2/3 are outlines).
- Slice 0 is DONE: `runner.go:519 goBuildArgs` = `dropFlags(args, true, goBuildRejected)` (`:573`), and `.lydite/components.yml:25` declares `-timeout 30m` on cli.
  - Verdict on `go-buildonly-passes-go-test-only-flags-to-go-build`: now-false (BuildOnly drops -timeout etc.).
  - Verdict on `cli-suite-outruns-gos-default-test-timeout`: now-false (timeout declared; not re-run to confirm the baseline passes).
- `agentic/references/mutation.md:381-395` ("no runtime budget ... dies as a CI job timeout") and `0027` "No runtime budget"/"no whole-repo mode" are still unamended although ADR 0075 says it reverses them.
- Line drift vs plan: `out.Interrupted = ctx.Err() != nil` is `run.go:253` (plan says 252); cutShort placeholders `executor.go:202,299-303,341-343`.
