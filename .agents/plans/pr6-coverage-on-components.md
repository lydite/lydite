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
row. Step 5 carries `internal/affected`, `internal/gitdiff`, `--affected`, the
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

Yours to settle in the challenge interview, with a recommendation for each:

- **What the coverage unit is when a component declares `command:`.** It opts
  out of the derived variants entirely, so there is no instrumented form to ask
  for. Decide whether such a component is unmeasured, excluded, or required to
  declare something, and note that `unmeasured` is a status that already exists
  and already does not vote.
- **What happens to `coverage.{go,rust}.report` and the `--*-report` flags.**
  They are keyed by discovered module/crate directory. If the unit is the
  component, the natural key is the component name — which is a rename of a
  published surface, and the old keys are in consumers' `.lydite/config.yml`
  today.
- **Whether the baseline's shape changes, and therefore its path.**
  `gitstate.StatePath` writes `v2/<key>.json` and the entries are
  language-keyed. Per-component entries are a different quantity, and ADR 0007
  already established that a changed quantity takes a new path and a clean cache
  miss rather than being read as the old one.
- **What `coverage.floor` gates now.** It is per measured unit today. A
  component is a coarser unit than a crate, so the same number means something
  different — decide whether that is a silent change in strictness.
- **Whether `lydite coverage` still exists as its own command.** A component's
  suite is run once; instrumenting it is a variant, not a second run. If
  `lydite test` can produce coverage, a separate command that re-runs everything
  is the duplication `coverage.source: report` was invented to avoid.

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

Step 5 took two review rounds where step 4 took seven, and the difference was
not luck — the core was pure functions over paths, with no goroutines and no
clock. What still bit:

- **A latent bug in code with no caller reads as working code.** The first
  review found that a path outside the scan root narrowed instead of widening,
  in a package nothing yet imported. Tests passed, lint passed, and the defect
  was one string comparison. Wire the thing up early enough that end-to-end
  behaviour is observable.
- **Injecting the defect is what establishes a test.** Twelve injections were
  run across that step and all twelve were caught, but two of them only proved
  their point after a lossy grep was fixed — the test had fired and the harness
  had hidden it. Read the actual failure output, not a filtered summary.
- **One hypothesis in that round was wrong.** An order-dependence predicted
  alongside a real bug did not exist; the bug masked it. Reproduce before
  fixing, and say so when the reproduction disagrees.
- **Assert what was skipped, not that the run was green.** The proving-ground
  assertion names the components that must *not* have run, because a selection
  returning everything satisfies every claim about correctness. The coverage
  equivalent is asserting which units were *not* measured.
- **Run it against the real repository.** The proving ground's declaration was
  read from GitHub rather than trusted from the prompt, and its component names
  (`tally`, `api`, `sdk`, `web`) differ from its directories (`rust`, `go/api`,
  `go/sdk`, `web`) — a constant naming a directory would have matched nothing
  and passed.

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
