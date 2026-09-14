# Session prompt — the history that cannot be recomputed

Continues `pr14-the-crap-index.md`, which shipped
[#108](https://github.com/lydite/lydite/pull/108) and
[#107](https://github.com/lydite/lydite/pull/107), and closed
[#16](https://github.com/lydite/lydite/issues/16) and
[#57](https://github.com/lydite/lydite/issues/57). Where the two differ, this file wins.

This slice is [#26](https://github.com/lydite/lydite/issues/26). It is the second of the
five the **cutover is held behind**, and it is second on purpose — see *Why this one now*.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. **Work in this worktree**
(`feature/acequia-flash-flood`) — do not create another one. It is currently detached at
`main`. Branch from there and point it at the new branch:

```sh
git checkout -b <type>/<name> dce1bd7   # main, after #108
```

`main` is `dce1bd7`. The root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user`
argument and use it for every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above it. Every
`go` and `golangci-lint` command runs from `source/cli` with `GOTOOLCHAIN=local`, **and
with the release build tags**:

```sh
GOTOOLCHAIN=local go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

Without them the binary carries all 206 of gotreesitter's grammars, and `ci-test` runs the
suite *with* them — a language whose own `grammar_subset_<lang>` tag is missing fails at
its first parse, and a bare `go test` can never see it.

## The gate on the release

**Nothing ships to consumers until secrets
([#20](https://github.com/lydite/lydite/issues/20)), licences
([#21](https://github.com/lydite/lydite/issues/21)), flaky tests
([#22](https://github.com/lydite/lydite/issues/22)) and the ledger (#26) are done.** CRAP
(#16) is now off that list; #17 remains open and is research.

`lydite/actions` `@v1` still points at `e936433` — the pre-shard commit — so consumers run
the old serial `test` job against the v0.1.0 binary. That pairing is consistent and
unbroken. **Do not move `@v1`, and do not tag a lydite release.** The order, when it
happens, is fixed: a lydite release shipping `test plan`/`test merge`/`test record` first,
because `setup@v1` resolves `latest`; only then does `@v1` move. Reversed, every consumer
calls `lydite test plan` on a binary that has no such command.

43 commits are unreleased since v0.1.0.

## Why this one now

A ledger entry **cannot be recomputed after the fact** — the pins that produced it may have
moved, and after a squash merge the commit it describes no longer exists. Every day lydite
runs without writing entries is history permanently lost, and the dashboard can be built
against whatever has accumulated while the data cannot be back-filled. That is the whole
argument for taking it before the three gates that could each be added at any time.

**#26 declares itself dependent on #16 *and* #17. Confirm that reading before relying on
it.** #17 changes which languages contribute to the scalars, not the scalars' shape — so
#16 alone should have fixed the schema, and #26 need not wait on research. If you find that
reading is wrong, say so and stop rather than designing around it.

## The slice

**#26, and only #26: the ledger and the projection.** Per
[ADR 0009](docs/adr/0009-quality-history-storage-and-access.md), which is the design —
read it before anything else. Append-only NDJSON partitioned by month on the `lydite`
branch, beside the per-commit baselines that are already there, plus a downsampled daily
rollup so the dashboard reads one small file.

**Not the dashboard.** `source/web/` is empty and ADR 0009's React app is a later slice.
Not the hosted read path. Not per-finding fingerprints, which ADR 0009 says are additive
later.

### Cache and Ledger are different policies, and that is the point

CONTEXT.md and ADR 0009 both draw this and the terms exist to keep it drawn. A **baseline**
is a *cache*: regenerable by re-running the tool, so a failed write costs time rather than
information and stays best-effort. **Quality history** is a *ledger*: never recomputable,
append-only, never deleted. Its writes are still non-fatal — failing a consumer's build
over a push race would erode trust in a gate otherwise about their code — so a failed
append is recorded as an explicit **Gap** and rendered as a break in the line. The
invariant is not "there are no gaps" but **the ledger never lies about its own
completeness**.

### What already exists

- `gitstate` writes `v4/<tree>.json` and `crap/v1/<tree>.json` in **one commit**, through
  the **single `gitstate.WriteBaseline` call site**, from `lydite test record` — the only
  job in either workflow with `contents: write`, and one that executes nothing from the
  repository. That invariant is checkable by grepping for the one call site and has been
  guarded through three pull requests. `gitstate.Snapshot` is the shape a tree's state
  takes; `ReadSnapshot` fetches once and reads every metric.
- **Coverage's scalars** are in hand: `gitstate.Entry` is `coverage.LineCount` plus a
  producer, per component.
- **CRAP's two scalars** are in hand: `gitstate.CRAPEntry{Above int, Worst float64,
  Producer string}`. `Above` is the count over the threshold and the only number the gate
  compares; `Worst` is recorded and never gated.

### What does not exist, and is most of the work

**Two of the issue's four scalars are collected by nobody.** Sizing this slice as "wire up
what is already measured" would be wrong.

- **Test counts.** `runner.Invocation.JUnitReport` is named by `cargo-nextest`, `vitest`
  and `jest` — and **not by `go-test`** — and *nothing anywhere parses any of them*.
  AGENTS.md already claims the ledger is why that path is named. So this is two jobs: a
  JUnit story for `go-test`, which has no built-in one (`gotestsum` and `go test -json` are
  the candidates, and a pinned tool is an ADR 0006 decision with a manifest and a
  Dependabot entry), and a parser for the XML the other three already write.
- **Finding counts.** `lydite scan` emits rows and counts nothing. Biome's findings reach a
  reader through `Result.Detail`; the others stream live and are never tallied.

### Decide these before writing anything

1. **Where the append happens, against the single-writer invariant.** The ledger is a
   different *policy* from the cache but the same branch and the same job, and `lydite test
   record` is the only thing holding a token that can push. Decide whether the append rides
   inside `WriteBaseline`'s one commit or is a second commit, and say why — a second
   *writer* is exactly what the invariant forbids, and a second *commit* is not the same
   thing. Whatever you choose, the grep must still answer.
2. **How a Gap is recorded when the append that would have recorded it is the one that
   failed.** ADR 0009 says a failed append is recorded as an explicit gap; if the write
   failed, nothing wrote. So a gap has to be *inferable* — a sequence number, or a
   last-seen marker the next successful append reconciles against. This is the subtle part,
   and it is the whole of "never lies about its own completeness".
3. **Whether mutation is a scalar.** ADR 0009 and #26 both predate `lydite mutation`.
   Killed, survived and unviable are scalars and are no more recomputable than the others.
   In or out — decide it out loud rather than by omission.
4. **What an entry is keyed by.** A baseline is keyed by *tree*, for a stated reason: a
   pull request and the commit it becomes share one. History wants a commit, a branch and a
   time. They are different keys for different reasons; do not copy one onto the other by
   reflex.
5. **Partition granularity against the 1 MB cap.** ADR 0009 wants monthly NDJSON with every
   file independently fetchable under the GitHub Contents API's 1 MB cap. At ~235 bytes an
   entry that is ~4,400 entries a month. Decide what a busier repository does.

## Owed from the last session

- **`docs/release-notes/v0.2.0.md` is on `main` and stale**, and more so than it was. It
  mentions mutation **zero** times; it names `.lydite-reports/baseline.json`, which is
  `measurements.json` and carries more than the old name said; it has no `lydite test
  plan`, `test merge` or `test record`; its closing `lydite/actions` section still reads as
  a to-do for work that has merged; and it now also lacks the CRAP gate, the
  `[lydite:exclude_from_<gate>]` grammar and ADR 0028. It must be correct **before** a tag
  is pushed — `release.yml` reads it out of the tagged tree — and the release is held, so
  there is time. **Its own pull request, not this one.**
- **[#109](https://github.com/lydite/lydite/issues/109) is filed and unstarted**: a mutant
  is bounded in time and not in memory, so an allocating loop takes the runner down before
  its deadline arrives. Measured: 14 GB in eighty seconds against a timeout that fired
  correctly at 1m46s.
- **[#103](https://github.com/lydite/lydite/issues/103)** is filed and unstarted: `lydite
  mutation` has no job in `lydite/actions`, so even after the cutover consumers get no
  mutants.
- **[#97](https://github.com/lydite/lydite/issues/97)** now has a third datapoint — 153
  mutants in 20m on `cli` with a warm Go build cache.

## Traps this session sprung

- **`main` moves under you.** #107 merged mid-session and GitHub retargeted #108 to `main`,
  so the PR's *merge* commit carried its base's content twice and failed to compile while
  every local check passed. Later `gt v1.7.0` landed and `ci-orchestration.yml` drifted.
  Rebase before believing a red `governance` or a compile error you cannot reproduce.
- **A dying mutation job does not look like a failing gate.** The step reports exit 143 and
  every `if: always()` step and post-step is `skipped`. Those run on a failure and on a
  cancellation alike, so a run where they do not is the runner going away — the OOM killer
  taking the agent. A job timeout, a cancelled workflow and a superseded `concurrency` group
  all leave the post-steps intact.
- **A mutation declaration must sit on the mutant's own line.** One written in the comment
  block above it covers nothing, and a doubled `// //` prefix leaves the token unrecognised.
  Both are silent: the survivor comes back looking untouched.
- **A test can pass for the wrong reason.** `nudgeWanted` was asserted against a pipe, and a
  pipe fails the terminal check, so all three cases were false for that one reason and two
  guards were never exercised. `/dev/null` is a character device and stands in for a
  terminal.
- **A merged PR is not a shipped one.** Check what a tag points at before claiming a
  consumer has something.
- **Pushing cancels the in-flight run** — `lydite-pr.yml` sets `cancel-in-progress`.
- **The `referral` job exits 0 on a referral.** Read `review referred in …` in the step's
  own output; a green job says nothing about the verdict.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.

## Environment

- `gh auth setup-git`, then push over **HTTPS** with the gh credential helper:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/lydite/lydite.git <branch>:<branch>`.
  SSH is blocked here, and a token must never reach a remote URL.
- **No container runtime here**, so nothing touching the proving ground runs locally.
- `lydite scan` runs from the repository root (`--dir ../..` from `source/cli`, or `--dir .`
  from the root). `--dir ..` names `source/`, which declares no components and errors.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to. **Never tag a release, and never move `@v1`.**
- Comments describe the code as it is — no "used to", no ticket numbers, no roadmap. ADRs
  are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- A new storage policy needs an ADR, or an amendment to 0009 if what you build differs from
  what it describes. The rejected alternatives are the decision's content.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the grammar
  tags**, from `source/cli`, plus `lydite scan --dir .` and `gt repo check` from the root.
- **Prove a regression test fails without its fix.** A test written after a fix and never
  seen red is a test that asserts the fix compiled.
- **Answer every mutation survivor**, and prefer rewriting past an equivalent mutant to
  declaring it: a declaration covers the boundary *and* the negation of one operator, so it
  stops counting a kill the tests are still making.
- Review before proposing and again after acting. Keep going while rounds return findings
  that change behaviour, and stop when what comes back is wording — and say so.

## The rest of the held list, in the order I would take it

- **#26** — this.
- **[#20](https://github.com/lydite/lydite/issues/20)** — secrets. Independent, and the ADR
  0006 shape is well-trodden: manifest, `dependabot.yml` entry, `.lydite/components.yml`
  exclude. Note the issue's last line — deleting a committed secret does not remove it from
  history, and the finding should say so rather than implying the fix is complete.
- **[#21](https://github.com/lydite/lydite/issues/21)** — licences. Needs a policy key in
  `.lydite/config.yml`, which is the one place a repo-level fact belongs. Delta, like CRAP.
- **[#22](https://github.com/lydite/lydite/issues/22)** — flaky tests. Independent, and it
  interacts with mutation: a flaky test makes mutation results meaningless, so it arguably
  gates mutation's inputs.
- **[#17](https://github.com/lydite/lydite/issues/17)** — the research. Longest pole, and
  the honest answer may be a hand-rolled cyclomatic walk per language.
