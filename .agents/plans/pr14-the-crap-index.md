# Session prompt — the first gate with a complexity term

Continues `pr13-the-consumer-cutover.md`, which shipped
[#105](https://github.com/lydite/lydite/pull/105) and
[lydite/actions#3](https://github.com/lydite/actions/pull/3), and closed
[#87](https://github.com/lydite/lydite/issues/87),
[#90](https://github.com/lydite/lydite/issues/90) and
[#104](https://github.com/lydite/lydite/issues/104). Where the two differ, this file wins.

This slice is [#16](https://github.com/lydite/lydite/issues/16). It is the first of five
the **cutover is now held behind** — see *The gate on the release* below before deciding
to do something else.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. Branch from `main`, currently
`d60769e`. The root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument
and use it for every `gh` command.

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

**Nothing ships to consumers until CRAP (#16, #17), secrets
([#20](https://github.com/lydite/lydite/issues/20)), licences
([#21](https://github.com/lydite/lydite/issues/21)), flaky tests
([#22](https://github.com/lydite/lydite/issues/22)) and the ledger
([#26](https://github.com/lydite/lydite/issues/26)) are done.** That is a deliberate
decision, not an oversight, and it is why the state below is a resting state rather than a
half-finished one.

`lydite/actions` `@v1` still points at `e936433` — the pre-shard commit — so consumers run
the old serial `test` job against the v0.1.0 binary. That pairing is consistent and
unbroken. **Do not move `@v1`, and do not tag a lydite release.** The order, when it
happens, is fixed: a lydite release shipping `test plan`/`test merge`/`test record` first,
because `setup@v1` resolves `latest`; only then does `@v1` move. Reversed, every consumer
calls `lydite test plan` on a binary that has no such command.

39 commits are unreleased since v0.1.0.

## The slice

**#16, and only #16.** CRAP for Go: `comp² × (1 − cov)³ + comp` per function, gating on the
**delta of the count above 30** against a baseline. Go is separable because complexity is
free here — lydite is Go and walks `go/ast` in-process, so there is no tool, no pin, no
install and no staleness risk. Rust and TypeScript are #17, which is a **research task**
and is not this.

It is a **Gate** in CONTEXT.md's sense: the author clears it by adding tests or decomposing
the function, both of which are the work you wanted. Not a Referral.

### Delta, and why not the other two shapes

Absolute would fail every repository on the day it upgrades, over debt it has always had —
the rule `coverage.floor` already follows by defaulting to `0`. Diff-scoping was considered
and rejected in the issue: a change can push a function over the threshold **without
touching it**, by complicating a call site or deleting the test that covered it, which is
the same blind spot aggregate coverage exists to cover. Delta against baseline is the only
one of the three that catches that.

### What already exists, and what does not

`internal/coverage.ParseGoProfile` (`patch.go:262`) already turns a component's Go profile
into `LineHits` — file to line to max count — and already drops generated files via
`isGeneratedGoFile` and non-executable lines via `nonExecutableLines`. That is the
generated-file exclusion #16 asks you to reuse; do not write a second one.

Per-function coverage is the intersection of a `FuncDecl`'s line range with that map:
covered is the lines in range with a count above zero, total is the lines in range present
at all. That is statement-shaped, which is how lydite already counts Go, so the two figures
cannot disagree about what a line is.

Complexity is a `go/ast` walk counting decision points. There is no existing walk to reuse
and no reason to want one.

### Where the baseline goes — decide this before writing anything

`gitstate.Entry` today is `coverage.LineCount` plus `Producer`, stored at
`v4/<tree>.json`. **Do not widen `Entry`.** Two reasons, and the first is a trap with a
silent failure mode:

- A `v4` entry carries no CRAP scalars, so a widened `Entry` read back gives them their
  **zero value**. The delta is then `current − 0`, which is the absolute count — so the
  first run after upgrade fails every repository over debt it has always had, which is the
  exact thing delta exists to prevent, arriving through the mechanism meant to prevent it.
  Widening therefore forces a `v5` bump.
- A `v5` bump costs every consumer a full **coverage** cache miss for a **CRAP** feature.
  The two quantities have different producers and different costs to re-measure, and
  coupling them makes every later CRAP change a coverage miss as well.

`gitstate.StatePath`'s directory is already documented as keyed to the metric and the unit
it is measured over. So give CRAP **its own keyed document**, missing-means-miss, and leave
`v4` alone. A repository with no CRAP baseline reports `new` and gates nothing for one
change, exactly as a changed producer already does — that shape is established and readers
know it.

### It will collide with #57, and that is worth knowing first

[#57](https://github.com/lydite/lydite/issues/57) is live on `main`, verified in code:
`measureBaseTree` (`cmd/lydite/coverage.go:665`) skips only `m.Measured()` and has no
`if m.Unmeasurable { continue }`, and `count()` (line 1396) counts unmeasurable components
in the composed `N of M` while `floorRows` (line 1310) deliberately excludes them. A clean
run already renders `coverage … 3 of 4 component(s)` beside `floor … 3 of 3`.

CRAP needs per-function coverage, which only a measured component has, so its own `N of M`
inherits the same question the moment you write it. Fix #57 first as its own pull request,
or write CRAP's counting to `floorRows`' rule and say in the PR that the coverage row is
still wrong. Do not quietly copy the defect.

## Scope

- A per-function CRAP computation for Go components, from the instrumented profile the
  coverage gate already produces. No second run and no second artefact — the same rule the
  patch gate follows.
- A gate on the delta of the count above 30, per component.
- Its own baseline document, keyed by tree as coverage's is, written only by
  `lydite test record` — the single `gitstate.WriteBaseline` call site is an invariant, so
  if CRAP needs a second writer, say so out loud in the PR rather than adding one quietly.
- The two ledger scalars #26 needs: **count above threshold** and **worst value**. Name
  them deliberately; #26's schema is fixed by this slice.
- Rows in the established grammar, and `--json` keys under
  `TestJSONKeysArePartOfTheContract`.

## What is deliberately not in scope

- **#17.** Rust and TypeScript have no per-function complexity source, and finding one is
  research with a real chance of ending at "hand-roll a cyclomatic walk per language".
  A `lang`-shaped abstraction invented now, from one implementation, is an abstraction
  fitted to Go.
- **A threshold key in `.lydite/config.yml`.** 30 is the standard CRAP threshold and the
  issue states it. A knob added before anyone has asked is a knob whose default is the only
  value anyone uses.
- **The ledger.** #26 depends on these scalars existing; it does not depend on this pull
  request writing any.

## Owed from the last session

- **`docs/release-notes/v0.2.0.md` is on `main` and stale**, in three ways that matter. It
  names `.lydite-reports/baseline.json`, which is now `measurements.json` and carries more
  than the old name said; it mentions mutation **zero** times, so `lydite mutation`,
  `mutation merge`, `//lydite:equivalent` and ADR 0027 are unannounced; and it has no
  `lydite test plan` or `lydite test merge`, while its closing `lydite/actions` section
  still reads as a to-do for work that has merged. The file has to be correct **before** a
  tag is pushed — `release.yml` reads it out of the tagged tree — and the release is held,
  so there is time. Fix it in its own pull request, not in this one.
- **AGENTS.md line 2600 calls `docs/release-notes/v2.0.0.md` the worked example.** No such
  file exists; it is `v0.2.0.md`. One line.
- **#97 is commented and open.** It now carries both ends of the cost scale — 314 mutants
  at ~10.3s each on `cli`, and 24 across three languages at 1.1–4.4s in the probe. The
  comment does **not** claim per-mutant cost is flat, because it is not; what the two
  datapoints support is that nothing grows with diff size. It also flags that the probe's
  three components barely overlapped — 55s of component time in a 54s run — which bears on
  the "shard mutation more finely" option and is not answerable from that log.
- **#103** is filed and unstarted: `lydite mutation` has no job in `lydite/actions`, so
  even after the cutover consumers get no mutants.

## Traps this session sprung

- **A merged PR is not a shipped one.** Both cutover PRs merged green and changed nothing
  for any consumer, because `@v1` had not moved. Check what a tag points at before
  claiming a consumer has something.
- **Cross-repository closing keywords need the full ref.** `Closes #90` in a
  `lydite/actions` PR names `lydite/actions#90`. It is `Closes lydite/lydite#90`.
- **`setup-go`'s `cache: true` does nothing without `cache-dependency-path`** when the
  module is not at the workspace root. It was silently broken in ten steps across seven
  workflows; the log line to grep for is `Dependencies file is not found`. Fixed, and worth
  remembering as a shape: a cache that misses is never a red run.
- **The `referral` job exits 0 on a referral.** Read `review referred in …` in the step's
  own output; a green job says nothing about the verdict.
- **Pushing cancels the in-flight run.** `lydite-pr.yml` sets `cancel-in-progress`.
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
- A new gate needs an ADR. #16 decides delta over absolute and over diff-scoping, and the
  rejected alternatives are the decision's content.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the grammar
  tags**, from `source/cli`, plus `lydite scan --dir .` and `gt repo check` from the root.
- **Prove a regression test fails without its fix.** A test written after a fix and never
  seen red is a test that asserts the fix compiled.
- Review before proposing and again after acting. Keep going while rounds return findings
  that change behaviour, and stop when what comes back is wording — and say so.

## The rest of the held list, in the order I would take it

- **#16** — this. No research, no pin, and it fixes the scalars #26 is waiting on.
- **#26** — the ledger, and it should come **second, not last**. Its own argument is that a
  ledger entry cannot be recomputed after the fact, so every day without one is history
  permanently lost. It declares itself dependent on #16 *and* #17, but #17 changes which
  languages contribute to the scalars, not the scalars' shape — so #16 alone fixes the
  schema. Confirm that reading before relying on it; if it holds, the longest-decaying
  item stops waiting on the longest-running one.
- **#20** — secrets. Independent, and the ADR 0006 shape is well-trodden: manifest,
  `dependabot.yml` entry, `.lydite/components.yml` exclude. Note the issue's last line —
  deleting a committed secret does not remove it from history, and the finding should say
  so rather than implying the fix is complete.
- **#21** — licences. Needs a policy key in `.lydite/config.yml`, which is the one place a
  repo-level fact belongs. Delta, like CRAP.
- **#22** — flaky tests. Independent, and it interacts with mutation: a flaky test makes
  mutation results meaningless, so it arguably gates mutation's inputs.
- **#17** — the research. Longest pole, and the honest answer may be a hand-rolled walk.
