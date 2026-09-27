---
about: migrating mutation/scan/review, publish/threads, status/mergequeue, and ledger onto Flow in parallel collides on two remaining shared seams — the SCMRepository interface plus its duplicated fakes, and the relay's write path being outside SCMRepository entirely — and publish itself does not fit the Flow shape at all; the third seam this note originally found (cmd-only toolchain/component helpers) was retired by #270's test migration
saw:
  - source/cli/cmd/lydite/test.go
  - source/cli/cmd/lydite/toolchain.go
  - source/cli/cmd/lydite/coverage.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/cmd/lydite/scan.go
  - source/cli/cmd/lydite/review_apisurface.go
  - source/cli/internal/test/run/run.go
  - source/cli/internal/test/run/env.go
  - source/cli/internal/forge/repository.go
  - source/cli/internal/stages/clearance/fake_test.go
  - source/cli/internal/stages/scm/scm_test.go
  - source/cli/cmd/lydite/status.go
  - source/cli/cmd/lydite/mergequeue.go
  - agentic/references/surface.md
  - agentic/references/architecture.md
  - source/cli/internal/stages/record/record.go
  - source/cli/internal/flows/record/record.go
  - docs/adr/0069-a-recordings-history-is-a-deferred-closure-and-its-inputs-cross-a-boundary-type.md
---

Synthesized while answering "what constrains splitting the remaining cmd/lydite migrations into
five parallel streams", cross-referencing the layered-refactor-constraints candidate, the
reviewdecision-Toolchains candidate and ADR 0060 against current code. **Updated** after #270
(the `test` Flow migration) landed: seam 1 below no longer holds as originally stated, since
that migration relocated exactly the helpers it named. Left in place with a correction rather
than deleted, because the seam's underlying question — where does a cross-command helper live
once one migration needs it as real logic — is answered by what #270 actually did, which is
useful precedent for the streams still ahead.

**Seam 1 — retired by #270.** `childEnv`, `splitPath` and `env` (`test.go:1981` at the time
this note was written) and `componentUnits` now live as real, exported logic in
`internal/test/run` (`env.go`, `run.go`), with `cmd/lydite/test.go`/`coverage.go` keeping only
thin same-signature wrappers of the same name, because `mutation.go`, `scan.go` and
`review_apisurface.go` — unowned by that migration — still call them by those names. A
`mutation`/`scan`/`review` Flow migration no longer needs its own prerequisite extraction of
these three: it can import `internal/test/run` directly, the way `internal/stages/test`'s own
stages already do, rather than reaching into `cmd/lydite`. `ensureToolchains` moved the same
way, into `internal/test/run`'s `EnsureToolchains`, with `toolchain.go`'s wrapper adapting away
`*cobra.Command` for `io.Writer`. Whichever of the remaining streams first needs one of these
should check `internal/test/run` before assuming a fresh extraction is still owed.

**Seam 2 — `forge.SCMRepository` (`internal/forge/repository.go:18`) is the interface every
Flow stage that touches the platform must go through (ADR 0060), and it is deliberately grown
one caller at a time, never speculatively.** `review`/`threads`/`publish` (surface concerns:
review threads, the standing comment) need methods the interface doesn't have yet — ADR 0060
says so explicitly ("review threads and the standing comment `lydite publish` writes are not
here because no stage reaches for them"). Widening it means editing
`internal/forge/repository.go`'s interface literal and its `var _ SCMRepository =
(*GitHubRepository)(nil)` implementation, plus **both** hand-written fakes that implement the
full method set today: `internal/stages/clearance/fake_test.go` and
`internal/stages/scm/scm_test.go` (confirmed distinct files, both named `fakeRepository`, no
shared embedding between them). Two streams widening the interface concurrently (e.g. one
stream's review-thread stage, another's status stage) conflict on the same interface literal
and on updating two duplicated fakes, not one.

**Seam 3 — the merge-queue write path is a third transport `SCMRepository` does not cover at
all.** `mergequeue.go`'s `queueDecision` posts to the `pr-relay` Worker over plain
`http.DefaultClient` (`mergequeue.go:130`), never through `forge.Client`/`SCMRepository` — the
relay is OIDC-authenticated app identity, a different credential model than the token
`GitHubRepository` wraps. A Flow migration of the mergequeue/status stream needs either a new
stage kind for "post via relay" or a decision that this path stays outside Flow, which is not
something the SCMRepository-widening seam (seam 2) resolves for free. `status.go` also still
builds `*forge.Client`/`forge.Repo` directly (`status.go:17-18,55`) rather than through
`NewGitHubRepository`/`trust.TrustedContext` — exactly the shape ADR 0060 rejected for stages
("stages taking `*forge.Client` and `forge.Repo` directly... nothing stops a future call site
from constructing a `Repo` from a flag or a payload instead of from trust").

**`publish` does not fit the Flow shape at all, so it isn't really a sixth thing to migrate
onto it.** `agentic/references/surface.md` line 16-21: "`lydite publish` renders and posts
nothing... No network, no token, no knowledge of a hosting platform." Flow exists for "a
command that answers a webhook by reading a platform live, deciding something, and writing back
to the platform" (architecture.md). `publish` reads report documents and renders; it has no
live platform read and (ordinarily) no write. Grouping `publish`/`threads` with `review` in one
stream should not default to wrapping `publish` in a Flow — only the pieces of that stream that
actually read/write the platform live (review's own comparison, threads' posting) are
candidates.

**`internal/ledger` writes (the fifth stream, `record`) were isolated, as predicted — confirmed
by its landed migration.** `lydite test record` now runs as `internal/flows/record` over stages in
`internal/stages/record`, and neither those stages, that flow, nor `cmd/lydite/record.go` imports
`internal/forge` or `internal/test/run`, or calls `ensureToolchains`/`childEnv(`/
`componentUnits(`: the stages read and write git-blob state (`internal/gitstate`,
`internal/ledger`) only. The one seam it did need is its own — another command's report document
reaching a stage without importing `package main` — resolved by a stage-owned `ReportReader`
interface and boundary types (ADR 0069), not by any of the shared seams above.
