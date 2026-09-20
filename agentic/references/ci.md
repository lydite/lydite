# CI

> **The reference for `.github/workflows/`** — which stages exist, what blocks a merge, and the failures CI has actually had.

gt renders `ci-orchestration.yml`, which calls one workflow per stage: `ci-preflight`,
`ci-build` (golangci-lint), `ci-test` (`go build`, `go test -race`) and `ci-end2end` (the
proving ground). `ci-gate` is the single check branch protection requires, and it waits on all
of them — so **what runs in a gt stage is what blocks a merge.**

**Everything lydite says about a pull request is `lydite-pr.yml`, and it is not a gt stage.**
The referral, the scan and the gated suites run in parallel and a `publish` job renders one
standing comment from all three (see [surface.md](surface.md)). It cannot be a stage: `gt repo config`
accepts exactly `preflight, build, test, end2end`, so there is no stage a publish could be, and
`statuses: write` cannot be granted to a called stage. The consequence is stated rather than
hidden — lydite's own coverage gate does not block a merge, which is the state `lydite/referral`
is already in and for the same reason ([#34](https://github.com/lydite/lydite/issues/34)).
A coverage regression and a scan finding are advisory here for the same reason; what that
costs and what closes it is [#75](https://github.com/lydite/lydite/issues/75). `ci-test.yml`
keeps the plain `go build` and `go test -race`, so the Go suite is the one thing lydite's own
merge gate still covers.

**A mutant is bounded in time and not in memory, and that is a real limit.** `--timeout` (and the
derived three-times-baseline default) says how long a mutant's suite may run; nothing says how much
it may allocate. A mutant that turns a bounded loop into an unbounded one takes the machine down
before its deadline arrives — measured on a hosted runner, 14GB in eighty seconds against a timeout
that fired correctly at 1m46s and was already too late. The engine cannot fix that for a consumer's
code ([#109](https://github.com/lydite/lydite/issues/109)); what lydite can do in its own is write
loops whose bound belongs to the loop rather than to a counter its body advances.

**The failure has a signature worth recognising**, because it does not look like a failing gate: the
step reports exit 143, and every `if: always()` step and post-step after it is `skipped`. Those run
on a failure and on a cancellation alike, so a run where they do not is a runner that went away
rather than a job that failed — and on a hosted runner that is the OOM killer taking the agent. A
job timeout, a cancelled workflow and a superseded `concurrency` group all leave the post-steps
intact.

**The mutation job streams, and the test matrix does not.** A mutation run prints nothing for as
long as it takes — every mutant's line goes to the component's log, and that log reaches a reader
through an artifact the job uploads on its way out. A job that is killed never gets there, so the
whole run is lost and nothing says how far it had come, which is exactly the case `--stream` exists
for. The test matrix keeps its output captured for the reason `internal/executil` gives, where `Run` and
`RunOutput` are told apart: a suite's log is thousands of passing lines, and a CI log carrying all
of them buries the one component that failed.

**Both workflows cache the Go build graph, and not only `~/.cache/lydite`.** `~/.cache/go-build`
and `~/go/pkg/mod` hold the toolchain `go.mod` names, the modules and the compiled dependencies;
without them every job downloads and compiles the whole graph from cold on every run, and mutation
pays that before its first mutant — the one job whose budget is a suite run repeated a hundred
times. The key is `go.sum` and never the commit: a per-commit key writes a multi-gigabyte entry on
every push and evicts the rest of the repository's cache to hold copies of one graph. The entry the
pull-request jobs read is filled by `lydite-baseline.yml`, since a cache written on the default
branch is visible to every branch and one written on a branch is not.

**Both lydite workflows shard.** `lydite-pr.yml` is `setup` → `plan` → `test` (a matrix, each job
running `--affected --component <slice> --gate-coverage --gate-flaky` under `contents: read`) → `merge` →
`publish`. `plan`'s output feeds a second matrix beside it — `mutation` → `mutation-merge`, each
job running `--affected --component <slice>` under the same read-only token — because a shard is
the same conflict closure whichever command consumes it, and mutation shares a checkout with the
coverage gate and not a compilation. `lydite-baseline.yml` is `plan` → `measure` (the same matrix without `--affected`, since
ADR 0016 requires the default-branch run to be complete) → one `record` job, which is the only job
in either workflow granted `contents: write` and runs nothing from the repository. A `scan` job runs
beside the matrix and feeds the same `record`: a finding count is one of the history's scalars and
nothing else in that workflow produces one. It passes **no** `--diff-base`, because on the default
branch there is no change to scope to — so it covers the whole repository, every claim lands
unanchorable, and the count is the standing total rather than what one change introduced. That is
also why it is the one lydite job needing no `fetch-depth: 0`: since ADR 0032 a base is resolved
whenever `--diff-base` is given, and this job gives none. A `mutate` matrix job runs beside
`measure` too, `contents: read`, scoped with `--base-sha HEAD~1` rather than a merge-base —
the merge commit is its own merge-base against the default branch, which is why `measure`'s
unconditional, unaffected run has no equivalent scoping need and `mutate`'s explicit one does —
and `record` folds its `mutants.json` documents into the ledger alongside `measure`'s
measurements and `scan`'s findings. It invokes `lydite mutation --no-gate`: a survivor on the merge
commit is recorded, not gated, so it no longer turns `lydite-baseline` red — the branch is gone by
the time anyone could act on it, and the remedy belongs to the pull request the survivor came from.
`lydite-pr.yml`'s own `mutation` matrix is unchanged and still gates; the gate belongs before the
merge, not after it. See
[ADR 0048](../../docs/adr/0048-a-post-merge-mutation-run-records-its-survivors.md).

Each shard uploads its report directory under `lydite-shard-<name>`, not `lydite-reports-<name>`:
`publish` reads the latter, and a shard's document rendered as a `test` section of its own would put
one section per shard under one heading, each answering about part of the repository. The baseline
scan uploads under `lydite-scan-repository` for the mirror-image reason — it is not a shard, and
`record` downloads both patterns into one directory so the fold walks them together.

`ci-end2end.yml`'s `proving ground — coverage gate` job is what holds all of that to a real
repository: the plan, a baseline miss measured in a throwaway worktree, the recording, the shards
that must then *hit* the cache, and the fold. It is the only job whose failure to record is
caught: `lydite-baseline.yml` runs `lydite test record` too, but on pushes to the default branch
alone, where a baseline nothing writes costs a slower run rather than a red one.

**It runs those steps twice, over a declaration of four components and over one narrowed to one.**
A cargo or npm workspace is one component by lydite's model, so a single-service repository plans
exactly one shard — and `actions/download-artifact` gives a lone match no subdirectory of its own,
extracting it straight into `path`. This repository declares two components and its own matrix
therefore always nests, so the flat layout every fold has to read is reached from no run here
unless a leg goes looking for it. The count is a matrix parameter and not a second job, because
between two jobs the narrow one is the one that stops being maintained. The one-component
declaration is derived from the proving ground's own — the kept component is named by the matrix
and each dropped component's directory becomes an exclude — so a rename over there moves the leg
with it instead of leaving orphans behind.

The legs differ in two places and nowhere else. The one-shard leg sends its shard through a real
`upload-artifact`/`download-artifact` round trip, which is what produces the flat layout its fold
then reads; the other folds the report directories the runner already holds. And the fold with one
shard's directory omitted — how a dead runner is exercised without one dying — belongs to the
four-component leg: omitting the only shard is a fold of nothing, which the other leg's own
emptiness check already covers. The assertions in `.github/assert-proving-ground.py` name the four
components the proving ground declares, so the one-component leg asserts inline what it can: one
shard planned, one baseline entry with counts and a producer, and one row each of `test(api)`,
`coverage(api)`, `coverage(repo)` and `patch(repo)` out of the fold.

That fold discovers its report directories the way `lydite-pr.yml`'s `merge` job discovers them,
and the two move together. Nothing calls one workflow's steps from another, so what the proving
ground holds is the shape of the discovery rather than the code performing it — a fold changed
there and not here leaves this leg failing against a layout the pull-request workflow has already
learnt to read.

The one-shard leg repeats this for `mutation-merge` and for `publish`, because each discovers its
report directories with its own independent check — `mutation-merge` by the document
(`mutation.json`) it finds, `publish` by whether a `lydite-reports-*`-named subdirectory exists at
all — and a change proven correct for `merge`'s discovery says nothing about the other two. The
mutation leg runs `lydite mutation` on the one declared component through the same
`upload-artifact`/`download-artifact` round trip and asserts the fold holds that component's row.
The publish leg goes further and builds both layouts on purpose: two real report artifacts
downloaded together, which nests, and a directory holding only a log and no document, uploaded
alone, which flattens — the exact shape of the regression this leg exists to catch, where a job
wrote logs and died before its document. `lydite publish` must still render that directory, as
"holds no report document", rather than drop the section, which is asserted directly on the
rendered comment rather than inferred from an exit code.

`ci-end2end.yml`'s `proving ground — mutation` job is the equivalent for the mutation engine, and it
needs one thing the others do not. A green `lydite mutation` says "nothing survived", which is what
an engine generating nothing reports too — so the probe plants two identical functions per
component, calls one from a test that asserts nothing and asserts every case of the other, and
requires every survivor to be in the first file and none in the second. The fixture is synthesised
onto a branch rather than committed to the proving ground: mutants come only from lines in the
change, so a survivor sitting on the default branch is in no later run's diff and the assertion
would pass forever having checked nothing. Rust and TypeScript run there or nowhere — Go needs no
worker directory, and this repository declares no Rust component.

`lydite scan --dir .` there is dogfooding rather than a formality: it is the only job that
exercises the scan/report path end to end against a real repository, and it caught a real bug
once (see the git history around the `go-version: "1.26.5"` pin below).

**Pin the exact Go patch version in workflows (currently `"1.26.6"`), never a bare minor (`"1.26"`).**
`actions/setup-go`'s `go-version: "1.26"` resolves to whatever `1.26.x` patch it has
cached/available, which is not necessarily the version this repo's `go.mod` `toolchain` directive
pins — and critically, `go install`-ing an *external* tool (gosec, govulncheck) does **not** consult
the current module's `go.mod` toolchain directive the way building the module itself does. This bit
us for real: the self-scan's `govulncheck` step passed locally (toolchain directive respected) but
failed in CI (setup-go had installed an older, vulnerable patch) until `go-version` was pinned to the
exact `1.26.5`. If `go.mod`'s `toolchain` line is ever bumped, update every `go-version-file:` reference
and any literal `go-version:` to match in the same change.

`internal/toolchain` (see [toolchains.md](toolchains.md)) now also sets `GOTOOLCHAIN` from the scanned repo's own
`go.mod` before running any Go tooling, which fixes this class of drift for **consumers** — they no
longer need to pin `go-version` on lydite's account. Keep the pins here regardless: this repo's
`ci-build` and `ci-test` jobs invoke `go` directly rather than through lydite, so nothing in that
mechanism covers them, and the self-scan benefits from the pin holding independently of the feature
it is dogfooding.

**`release.yml` is release infrastructure, not a third sharded workflow, and shares no
machinery with `lydite-pr.yml`/`lydite-baseline.yml` above.** It is not a `gt` stage either — it
triggers on a `v*.*.*` tag push, never a pull request, and has no `plan`/`test`/`merge`
matrix. Its `goreleaser` job runs `lydite release check --dir ../..` as a step ahead of
goreleaser itself, reading the tag being released and the commit range back to the previous
release for a declared break its bump does not admit; a failure there stops the release before
goreleaser publishes anything. That step runs in `goreleaser` and not `build-test` because only
`goreleaser`'s checkout already sets `fetch-depth: 0` and fetches the tags a range walk needs.
See [ADR 0045](../../docs/adr/0045-a-tag-that-is-not-the-breaking-bump-cannot-carry-a-declared-break.md)
for the rule it enforces.


