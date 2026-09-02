# Session prompt — step 6: coverage onto components

`lydite coverage` measures ecosystems it discovers by walking for manifests.
The component is now the unit lydite builds, tests, schedules and selects, and
coverage is the one gate still using a parallel notion of what a unit is. ADR
0016 has it move; this is that move, plus the two things that cannot land
without it.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

    git fetch origin main
    git switch -c feat/coverage-on-components origin/main

**Verify steps 1, 2, 4 and 5 are merged first.** Step 1 (#39) carries
`internal/component`, `internal/runner`, `internal/compose` and `lydite test`.
Step 2 (#42) carries `internal/orphan`, `internal/pathmatch` and `excludes`.
Step 4 (#44) carries `internal/scheduler`, `--concurrency` and the `schedule`
row. Step 5 (#46) carries `internal/affected`, `internal/gitdiff`, `--affected`, the
`select` and `watch` rows and
[ADR 0018](../../docs/adr/0018-selection-widens-on-ignorance.md). If any is
missing, stop and say so.

**First commit: correct the plan.** `.agents/plans/component-platform.md`
should say step 6 is in progress and name this file as its prompt.

## Read before planning

- `docs/adr/0016-components-and-lydite-run-tests.md` — the component is the
  unit. Do not re-litigate it.
- `docs/adr/0002-go-multi-module-coverage.md` and
  [ADR 0007](../../docs/adr/0007-line-weighted-coverage-aggregation.md) — how
  units are discovered and aggregated today.
- `docs/adr/0003-coverage-source-in-config.md` — the axis being deleted.
- `AGENTS.md` — the whole Coverage section, and Components.
- `CONTEXT.md` — `Component`, `Baseline`, `Aggregate coverage`, `Patch
  coverage`, `Gate`.
- `cli/internal/coverage/` — `Compute`, `Unit`, `LineCount`, `PatchSources`.
- `cli/internal/runner/` — the instrumented variant already exists per runner.

## The task

Three things, and they are one change because none of them survives alone.

**Coverage measures components.** `internal/detect` stops deciding what a unit
is; the declaration does. A component's instrumented variant already exists in
`internal/runner` — `go-test` appends `-coverprofile` and `-coverpkg=./...`,
`cargo-nextest` becomes `cargo llvm-cov nextest` exporting `--json` and
`--lcov` from one run — so coverage stops assembling an invocation and starts
asking the runner for one.

**`#36`'s `-coverpkg` reset lands here.** Without `-coverpkg=./...` Go
instruments only the package under test, so code exercised solely through
another package's tests reads as uncovered and a pull request whose new code is
fully exercised from its caller fails the patch gate on correct work. The
component is what makes the flag's scope statable at all: `./...` means the
component, not the repository.

**`cargo-llvm-cov` is installed, at last.** It is the gap that poisons a
baseline under `coverage.source: run` — `internal/coverage.rustCoverage`
requires it on PATH and lydite does not install it, so on a runner without it
the baseline computes to `{}`, and a cached `{}` makes every later pull request
a cache hit that gates on nothing. `internal/cargotool` already holds the
version parsing and the version-keyed install that `internal/rust` and
`internal/runner` share; this is a third caller, not a third mechanism.

**`coverage.source` is removed.** It exists because lydite had no way to know
whether a repository's pipeline already produced coverage. A component declares
its runner, so the instrumented variant is derivable and the axis has nothing
left to say. Removing a config key that repositories have set is a breaking
change: it needs a release note, and the key must be *rejected* with an error
naming the removal rather than silently ignored — the stance
`config.validateLinter` takes for `linter: eslint`, for the reason that a
silently dropped key means a repository measuring something other than what its
author wrote while every run still reports a pass.

## Settled — the challenge interview is done, do not reopen these

Recorded in [ADR 0019](../../docs/adr/0019-coverage-per-component-gated-by-lydite-test.md),
with `CONTEXT.md`'s **Aggregate coverage**, **Baseline**, **Affected** and
**Invalidator** entries updated and [ADR 0018](../../docs/adr/0018-selection-widens-on-ignorance.md)
amended. Read ADR 0019 first; it carries the reasoning and the rejected
alternatives.

1. **`lydite coverage` is removed.** `lydite test` measures coverage always;
   `--no-coverage` opts out of instrumentation and emits no coverage rows at all;
   `--gate-coverage` adds the baseline read/compare/record. Measuring is local,
   gating touches the network, and a local run must never push to the `lydite`
   branch — so the flag is explicit, the way `--affected` is.
2. **A run that measured but did not gate renders distinctly from a pass.** This
   is the wardnet#957 distinction and the reason a flag was acceptable at all.
   Do not let ungated rows disappear or read as green.
3. **A baseline is per-component line counts at `v3/<tree>.json`.** Counts, not
   percentages: a percentage cannot be re-weighted, and the per-language and
   global figures are `Σ covered / Σ total` over subsets of the same entries.
   Three altitudes — per component, per language, global — all gated, all derived
   from one stored quantity so they cannot disagree.
4. **A composed figure names what it measured.** Under `--affected` the language
   and global figures mix fresh counts with carried-forward ones; the row says
   how many of each, the `N of M unit(s)` shape `floorReport` already uses.
5. **No run on the default branch is required.** Tree-keying carries the baseline
   chain through pull requests: CI builds `refs/pull/N/merge`, a squash merge
   lands that same tree, and the next pull request's merge-base resolves to it.
   Requiring a main run is a consumer obligation a PR-only pipeline never meets.
6. **A cache miss recomputes, and never substitutes.** Check the base tree out
   into a throwaway worktree and measure it through the same path `lydite test`
   uses, so the compose services a component declares actually start. Gating
   against the nearest ancestor baseline was considered and rejected.
7. **`coverage.floor` gates per component.** A change in the unit, not a
   weakening — an untested crate still contributes uncovered lines to its
   component. What is lost is catching a *small* untested sub-unit. Say so in
   the release note; do not claim nothing changed.
8. **Removed keys and flags, each rejected with an error naming the removal:**
   `coverage.source`, `coverage.go.report`, `coverage.rust.{report,lcov}`,
   `--source`, `--tests`, `--go-report`, `--rust-report`, `--rust-lcov-report`.
   Never silently ignored.
9. **Patch coverage gates per component** against that component's own baseline,
   from the same instrumented run.
10. **A `command:` component is `unmeasured`** — it has no instrumented variant.
    Not excluded, which would drop it from the global figure silently. No new key
    naming where its coverage lands.
11. **Rust exports lcov only; the `--json` export is dropped.** One artefact per
    language serving both gates: Go's profile, Rust's lcov, TypeScript's lcov.
    `coverage-summary.json` goes too.
12. **`gitstate.BaseSHA` takes a base ref.** Resolution order: an explicit
    `--base-branch`; then `git symbolic-ref refs/remotes/origin/HEAD`; then
    whichever of `origin/main` / `origin/master` exists, erroring if both or
    neither do; then error naming the flag. The remote stays `origin`
    deliberately — half-solving it is worse than naming the limit.
13. **A `.`-rooted component no longer suppresses affected selection's widening.**
    A path counts as matched only when a component with a non-`.` directory
    contains it or a `watch` names it; `.`-rooted components are still selected by
    containment. The cost is accepted and large: in a `.`+`web/` repository nearly
    every change now widens.

### Verified against the tools, not assumed

Four things were checked directly during the interview. Re-verify rather than
trust this list if anything depends on it.

- **`internal/runner`'s Rust instrumented variant cannot run.** It builds
  `cargo llvm-cov nextest --json --output-path X --lcov --output-path Y`, and
  cargo-llvm-cov 0.6.16 answers `error: the argument '--output-path' was provided
  more than once`. It fails at argument parsing, before anything executes.
  `TestCargoInstrumentedExportsBothReports` asserts both flags are present and
  passes, because the runner tests assert argv and never execute. Nothing else
  reaches it: `lydite test` runs the *plain* variant for `cargo-nextest`, and the
  only route in is `runner: cargo-llvm-cov-nextest`, which the proving ground does
  not declare. Decision 11 is the fix.
- **An lcov's summed `LF`/`LH` equal the JSON export's
  `totals.lines.{count,covered}`** — measured 10/7 both ways on a one-crate
  workspace. Confirm this on the proving ground's three-crate `rust/` component
  before relying on it; a discrepancy there costs a cache miss, not a wrong gate,
  since `v3` is new anyway.
- **#36 reproduces at 75 points.** A two-package module where `b` is exercised
  only through `a`'s test: without `-coverpkg=./...` the total is 25.0% and `b`
  reads 0.0%; with it, 100.0%. `internal/runner` carries the flag and
  `internal/coverage` does not, which is why consolidating on the runner is the
  fix rather than a tidy-up.
- **`-coverpkg` emits duplicate blocks, and this is NOT a bug.** A package
  instrumented by several test binaries appears several times in the profile. The
  hypothesis that `goProfileLines`'s `total += b.NumStmt` double-counts was
  wrong — `cover.ParseProfiles` merges them, and both profiles compute 1/4 =
  25.0%, matching `go tool cover -func`. No change needed.

### CI edits are approved, on this basis

`.github/assert-proving-ground.py` and, only if an extra probe step proves
necessary, `.github/workflows/ci-end2end.yml`. Assert the **negative**: `rust/`
reports as **one** unit and not three, `web/` as one and not two, `go/api` and
`go/sdk` as two; under `--affected` with a change under `go/api/`, `tally` is
**not measured**; and the measured-but-not-gated state renders distinctly from a
pass. Asserting the run was green proves nothing.

Asserting the *gate* end to end is out of scope — it needs a baseline on the
proving ground's own `lydite` branch and a token with push rights to it. File it.

### Follow-up issues to file, not to build here

- The action should pass `GITHUB_BASE_REF` on `pull_request` events, which is what
  actually fixes stacked pull requests. That is `lydite/actions`; decision 12 only
  makes the flag exist and stops the assumption.
- The end-to-end gate assertion on the proving ground, above.

## The failure class this step is most exposed to

Every step so far produced one shape: **a check that could not run reading as a
check that passed.** Coverage has the worst recorded history of it in this
repository, and none of it was hypothetical:

- A `{}` baseline cached as if real, making every later pull request a cache
  hit that gated on nothing — silently, permanently, with no way to self-heal.
  wardnet ran that way across all nine of its baselines.
- Go coverage measured at a monorepo root, where `go test` is module-scoped and
  measured *nothing*, reported as a warning while CI stayed green.
- The patch gate skipped for want of an lcov path, rendering as "patch coverage
  passed" in the PR comment while Codecov failed the same diff.

Every one of those is the same bug. Assume this change reintroduces it
somewhere and go looking, rather than waiting for a test to say so.

### What step 5 learned, and what it cost

Step 5 took **four review rounds and produced fourteen findings**, where step 4
took seven rounds. None of the fourteen was a regression — nothing a later round
found was a gap in an earlier fix — which is the real difference from step 4 and
supports the theory that step 4's tail was concurrency-specific. But the rate did
not fall until the end: two findings, then six, then five, then three that turned
out to be one bug. **Round 3 found the worst defect in the slice.** Budget for
four rounds here and do not read a quiet second round as a finished one.

What bit, and will bite again:

- **A latent bug in code with no caller reads as working code.** The first round
  found that a path outside the scan root narrowed instead of widening, in a
  package nothing yet imported. Clean build, clean lint, passing suite, and the
  defect was one string comparison. Wire new code to a caller early enough that
  end-to-end behaviour is observable.
- **Injecting the defect is what establishes a test — and the harness can hide
  it.** Twenty-four injections were run and all twenty-four were caught, but one
  injection left a variable unused, so nothing compiled and no test ran at all.
  The filtered output showed nothing, which reads exactly like a passing
  injection. Read the actual failure text, never a grep summary.
- **Reproduce before fixing, and check what you actually ran.** One review
  finding was partly wrong; one hypothesis raised alongside a real bug did not
  exist, because the bug masked it; and one "fix" was verified against a stale
  binary, because a `cd` had failed silently and the run never used the new code.
- **A rule that lives only in prose is a rule that is not true.** "The default
  branch runs everything" sat in AGENTS.md for three rounds before anything
  enforced it. Enforce a rule in the code, or expect to find it violated.
- **Assert what was skipped, not that the run was green.** The proving-ground
  assertion names the components that must *not* have run, because a selection
  that quietly returned everything satisfies every claim about correctness. The
  coverage equivalent is naming the units that must **not** have been measured.
- **Run it against the real repository.** The proving ground's declaration was
  read from GitHub rather than trusted from the prompt, and its component names
  (`tally`, `api`, `sdk`, `web`) differ from its directories (`rust`, `go/api`,
  `go/sdk`, `web`) — a constant naming a directory would have matched nothing and
  passed.

## Validate it against the proving ground

`lydite/proving-ground` has the shapes this needs: `go/api` and `go/sdk` are
separate modules, `rust/` is one workspace of three crates, `web/` is one npm
workspace of two packages. So the four components span exactly the three cases
where "the component" and "what `internal/detect` would have found" disagree —
one component holding three crates is the one that matters most, since the
line-weighted aggregate must not change meaning when the unit does.

Extend `.github/assert-proving-ground.py`, which already holds the orphan gate,
the scheduler's observed concurrency, and selection's chosen and skipped sets.
**Editing CI requires asking the user first.**

## Two gaps step 5 left open, to be closed here

Both are in scope. **Both are now settled** — decisions 12 and 13 above carry the
answers; what follows is the evidence behind them.

**`gitstate.BaseSHA` hardcodes `origin/main`** — the remote *and* the branch, in
both the `git fetch origin main` and the `git merge-base HEAD origin/main`. Step
5 only widened the error message (`cmd/lydite/test.go`'s `selectAffected` names a
non-`main` default branch as one of two causes), so `--affected` still cannot run
on a repository whose default branch is `master`, and neither can `lydite
coverage`, `lydite scan --diff-base auto` or `lydite review --base auto` — all
four resolve through the same function. That is why it lands here rather than in
step 5: fixing it changes how the coverage baseline resolves its base, which is
this step's subject. Decide how the default branch is discovered (`git
symbolic-ref refs/remotes/origin/HEAD` is not set by `actions/checkout`, so a
discovery that only works locally is worse than none) and whether the remote is
discovered too, and make the fallback loud rather than silent.

**A component rooted at `.` switches the widening rule off.** `affected.under`
returns true for every path when `dir` is `"."`, so `matched` is always true, the
`KindUnmatched` branch is unreachable in such a repository, and only the built-in
invalidator set protects the other components. ADR 0018 records this as a limit
of the rule rather than a hole closed. Closing it means deciding what a path
matched *only* by the catch-all root should count as — the widening direction
says treat it as unmatched and select everything, which is safe and costs such a
repository the optimisation almost entirely. Whatever is decided, it must be
enforced in code: step 5's lesson is that a rule living only in prose is a rule
that is not true.

## Out of scope

Scan onto components and deleting `internal/detect` (step 7) — coverage stops
*using* it here; deleting it is that step. `lydite test plan`/`lydite test
merge` and the reusable workflow (step 8). All of mutation (step 9).

## Open questions earlier steps left, yours only if you touch them

- **`typescript.install` and `setup` overlap.** Repository-wide in
  `.lydite/config.yml` versus per component. Coverage's TypeScript path is the
  one that installs, so this step is where it becomes decidable.
- **Toolchains resolve per ecosystem, not per component.** Node is the real gap
  — one runtime at the highest `engines.node` across every package. It belongs
  with step 7; do not start it, but do not make it worse.

## Non-negotiable workflow rules

- Before calling `ExitPlanMode`, invoke the `challenge` skill and complete its
  interview. A plan that has not been challenged is incomplete.
- Commits and PR bodies must NEVER contain `Co-Authored-By` lines, a
  `Claude-Session` trailer, or any `claude.ai/code/session_...` URL.
- Use `Closes #N` on PRs, not `Refs`. If a PR does not deliver an issue's whole
  scope, file a new issue for the remainder first, then close the original.
- Never merge a PR unless explicitly told to.
- Comments describe the code as it is, never the change that produced it. No
  "used to", "previously", "no longer", "Regression:", no roadmap talk, no
  ticket numbers. ADRs are the only exception. This is the repo's
  most-violated rule.
- **Never hand-edit a generated file.** `.github/dependabot.yml` and the gt
  workflows are rendered from `.gt-repo.yaml`; edit the source and run
  `gt repo sync`, then `gt repo check`. `ci-end2end.yml` is not one of them —
  its header says so — but check before editing any other workflow.
- Before proposing a PR: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `cli/`, all clean. Also `lydite scan --dir .`
  and `gt repo check`.
- Ask before editing CI.

## Working mode

- Verify a claim before making it, against the repository rather than against
  this file.
- Run it against a real repository, not a described one. A hand-written report
  fixture agrees with whatever the code does.
- Wire new code to a caller early. A package nothing imports can hold a defect
  through a clean build, a clean lint and a passing suite.
- Run `/code-review` before proposing the PR, and again after acting on it.
  Keep going while rounds return findings that change behaviour. Stop when what
  comes back is wording and test robustness, and say plainly that the trend is
  the reason rather than claiming the work is clean.
- Prefer one shared implementation over two that agree today.
  `internal/cargotool`, `internal/nodedeps`, `internal/download`,
  `internal/pathmatch` and `internal/gitdiff` all exist for that reason.
- Do the unambiguous work first, and raise a genuine fork in the road as a
  question at the point it actually blocks something.

## Finally

Update `.agents/plans/component-platform.md` to mark step 6 done, and write
`.agents/plans/pr7-scan-on-components.md` as the prompt for the next session,
in the same shape as this one: scan learning its units from the declaration,
`internal/detect` deleted, and per-component toolchain resolution closing the
Node gap that has been deferred since step 1.
