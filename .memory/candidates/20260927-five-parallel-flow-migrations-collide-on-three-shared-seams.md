---
about: migrating mutation/scan/review, publish/threads, status/mergequeue, and ledger onto Flow in parallel collides on the SCMRepository interface plus its duplicated fakes; the relay's write path stays outside SCMRepository by design (internal/relay, ADR 0075), publish runs as a flow despite no live platform I/O (ADR 0074), and the cmd-only toolchain/component helper seam was retired by #270's test migration
saw:
  - source/cli/cmd/lydite/test.go
  - source/cli/cmd/lydite/toolchain.go
  - source/cli/cmd/lydite/coverage.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/internal/stages/mutation/mutation.go
  - docs/adr/0065-a-stage-reports-its-outcome-as-data-and-the-cli-alone-decides-the-rows.md
  - source/cli/cmd/lydite/scan.go
  - source/cli/cmd/lydite/review_apisurface.go
  - source/cli/internal/test/run/run.go
  - source/cli/internal/test/run/env.go
  - source/cli/internal/forge/repository.go
  - source/cli/internal/stages/clearance/fake_test.go
  - source/cli/internal/stages/scm/scm_test.go
  - source/cli/cmd/lydite/status.go
  - source/cli/cmd/lydite/mergequeue.go
  - source/cli/internal/relay/relay.go
  - docs/adr/0075-the-merge-queue-submission-is-a-flow-over-a-relay-client.md
  - docs/adr/0074-publish-runs-as-a-flow-of-ordinary-stages.md
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

The `mutation` migration (`internal/flows/mutation`, `internal/stages/mutation`) is landed and
did not import `internal/test/run` from its stages: `RunMutants` needs the per-component
lifecycle `lydite test` runs (plan and log, `prepare`, compose services, setup/teardown), and
those helpers answer a decided `ui.Row`, which a stage may not produce. It declares
`mutationstages.Shape`, `Lifecycle` and `Toolchains` interfaces instead
(`internal/stages/mutation/mutation.go`), and `cmd/lydite/mutation.go`'s `mutationShape`,
`mutationLifecycle` and `commandToolchains{cmd}` implement them over the same `cmd/lydite`
wrappers — so `mutation.go` still calls `childEnv`, `invocation`, `langOf`, `prepare`,
`startServices`, `runCommands` and `planComponents` by those names (see ADR 0065 and
`agentic/rules/a-row-a-shared-helper-already-decided-crosses-into-a-stage-as-an-opaque-error.md`).

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

**Seam 3 — the merge-queue write path is a transport `SCMRepository` does not cover, and is not
meant to.** The queue flow (`internal/flows/queue`) submits through `internal/relay`, its own
domain package over an injected `relay.Doer` — OIDC-authenticated app identity, a different
credential model than the token `GitHubRepository` wraps. ADR 0075 records why it is neither an
`SCMRepository` implementation nor a Sink: the job holds no token and no repository for
`init-trust`/`init-scm` to model. `status.go` also still
builds `*forge.Client`/`forge.Repo` directly (`status.go:17-18,55`) rather than through
`NewGitHubRepository`/`trust.TrustedContext` — exactly the shape ADR 0060 rejected for stages
("stages taking `*forge.Client` and `forge.Repo` directly... nothing stops a future call site
from constructing a `Repo` from a flag or a payload instead of from trust").

**`publish` runs as a Flow of ordinary stages despite having no live platform read or write.**
`internal/flows/publish` wires gather-reports, build-comment and write-comment; ADR 0074 records
that it was put on the scaffold for structural consistency, and that no Source/Sink concept exists
in the engine.

**`internal/ledger` writes (the fifth stream, `record`) were isolated, as predicted — confirmed
by its landed migration.** `lydite test record` now runs as `internal/flows/record` over stages in
`internal/stages/record`, and neither those stages, that flow, nor `cmd/lydite/record.go` imports
`internal/forge` or `internal/test/run`, or calls `ensureToolchains`/`childEnv(`/
`componentUnits(`: the stages read and write git-blob state (`internal/gitstate`,
`internal/ledger`) only. The one seam it did need is its own — another command's report document
reaching a stage without importing `package main` — resolved by a stage-owned `ReportReader`
interface and boundary types (ADR 0069), not by any of the shared seams above.
