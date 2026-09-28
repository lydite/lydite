---
about: a component's declared `-timeout` still reaches BuildOnly's `go build` (breaks it), and mutation's baseline `go test` run carries no timeout override of its own
saw:
  - source/cli/internal/runner/runner.go
---

Investigated while answering whether issue #289 ("cli baseline outruns go test's
10-minute default; declared -timeout breaks BuildOnly's go build") is fixed on `main`.
No reference to "289" exists anywhere in the repo (code, docs, agentic/), and
`git log` has nothing matching either complaint, so there is no record of it being
addressed.

Traced `dropCoverage` (`runner.go:519-544`), which both `goTestFlags` and
`goTestUninstrumented` share. It only strips `coverageFlags` (`cover`,
`coverprofile`, `coverpkg`, `covermode`, `runner.go:551-556`) — `-timeout` is not
in that table, so it survives into `buildGoTest`'s `BuildOnly` case
(`runner.go:400-401`), which runs `go build` with whatever survived
`goTestUninstrumented`.

Verified directly: `go build -timeout 5m .` in a scratch module fails with
`flag provided but not defined: -timeout`. So a component that declares
`args: ["-timeout", "5m"]` (or any `-timeout`) still breaks its own mutation
`BuildOnly` phase (and coverage's build-only phase, which shares `buildGoTest`)
today — the second half of #289 reads as unfixed.

Also checked for a default/override on the *instrumented* (baseline) invocation
that `buildGoTest`'s `Instrumented` case builds (`runner.go:377-399`, the
`gotestsum -- -coverprofile=... -coverpkg=./...` form): nothing in `runner.go`
adds or overrides `-timeout` there, so a component whose baseline suite runs
longer than `go test`'s own default (10 minutes) and declares no `-timeout` of
its own is still subject to that default, unrelated to anything
`internal/stages/mutation/budget.go` derives — the derived per-mutant budget
comes *from* the baseline's measured elapsed time, which presupposes the
baseline finished. The first half of #289 also reads as unfixed, though this
is inferred from absence rather than from reproducing the timeout itself.
