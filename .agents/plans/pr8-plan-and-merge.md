# Session prompt — step 8: `lydite test plan`, `lydite test merge`, and the reusable workflow

> **Not the next session.** Step 8 is deferred behind
> [#62](https://github.com/lydite/lydite/issues/62) and
> [#64](https://github.com/lydite/lydite/issues/64), taken as one slice: nothing
> lydite scans or measures reaches a pull request, and nothing records the
> identity that would post it. Every gate built so far is invisible where it
> matters. Read this when that slice has landed — and check what it changed,
> since a surface that publishes results is one the planner and the merge step
> both have to feed.

Every component now runs, is measured, is gated and is scanned through one
declaration. What is still hand-maintained is the *distribution*: a consumer's
CI has to know how to fan components out across matrix jobs and how to put the
answers back together. This is the step that makes a consumer's CI one `uses:`.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

    git fetch origin main
    git switch -c feature/plan-and-merge origin/main

**Verify step 7 is merged first.** It carries the deletion of
`internal/detect`, scan on components, per-component toolchains,
`toolchain.Compose` and [ADR 0020](../../docs/adr/0020-scan-on-components.md).
If it is missing, stop and say so.

**First commit: correct the plan.** `.agents/plans/component-platform.md`
should say step 8 is in progress and name this file as its prompt.

## Read before planning

- [ADR 0017](../../docs/adr/0017-shards-the-scheduler-and-the-planner.md) — a
  matrix job holds a **shard**, not a check, and the grouping is lydite's to
  compute. This is the specification; do not re-litigate it.
- [ADR 0019](../../docs/adr/0019-coverage-per-component-gated-by-lydite-test.md)
  — what a baseline is, why it is counts and not percentages, and why a
  partial record is worse than none. `merge` is the command that has to fold
  several runs' worth of those together.
- `CONTEXT.md` — **Shard**, **Scheduler**, **Component**, **Cache**, **Ledger**.
- `cli/cmd/lydite/test.go` — `runComponents`, the `schedule` row, and how rows
  are ordered.
- `cli/internal/scheduler/` — the port-conflict predicate the planner has to
  share rather than reimplement.
- `.github/workflows/ci-end2end.yml` and `.github/assert-proving-ground.py` —
  what is already asserted against the proving ground, and how.

## The task

**`lydite test plan`** emits the shard matrix: which components each job runs.
It reads the same declaration everything else does, groups components into
shards, and emits something a GitHub matrix can consume directly. Two
constraints are already settled and are the reason the planner exists at all:
components publishing the same host port must not land in different jobs
expecting to run at once, and a shard holding several components is what keeps
the scheduler's port lock exercised anywhere automated. `internal/scheduler`
already holds the port-conflict predicate — share it rather than writing a
second one that agrees today.

**`lydite test merge`** folds the shards' reports into one. Each job produced a
`--json` document over a subset of components; the merged document is what the
verdict, the composed coverage figures and the PR comment are computed from. The
hard part is not the folding but what a *missing* shard means: a job that never
ran, or was cancelled, or whose runner died, must not produce a merged document
that reads as a complete run over fewer components — the failure ADR 0019
already refuses for a partial baseline, one level up.

**The reusable workflow** makes a consumer's CI one `uses:`. Today a consumer
wires `plan` into a matrix, runs shards, and merges — the workflow is what
stops every consumer maintaining that by hand and drifting.

## What step 7 learned, and what it cost

Step 7 was smaller than step 6 and its lesson was sharper: **the defect that
mattered was invisible to every test that did not launch a process.**

`Env.Activate` wrote the resolved toolchain into lydite's own environment.
Deleting it — necessary, because components run concurrently and a process-wide
`PATH` holds one Node version — silently broke every provisioned toolchain,
because `os/exec` resolves a bare program name against *this* process's `PATH`
when the command is constructed, and `cmd.Env` is applied afterwards with no
bearing on it. The build was clean, the lint was clean, the whole suite passed,
and every argv assertion still held. It surfaced only by running `lydite test`
with `PATH` stripped of node: lydite reported `installed Node v22.11.0` and then
`npm ci: executable file not found in $PATH` three lines later.

What transfers:

- **Compose an environment in one place, and make the lookup agree with it.**
  A child's environment is a flat list where the last occurrence of a key wins,
  so two callers each prepending to `PATH` produce two entries and one is
  discarded with nothing to show for it. `toolchain.Compose` is that one place;
  `runner.Invocation` carries directories rather than a finished string.
- **Run it with the thing missing.** The ambient toolchain satisfied every
  requirement on the development machine, so the provisioning path never
  executed. `env -i PATH=/usr/bin:/bin` is what made it execute, and that is
  three seconds of work that no amount of reading found.
- **Delete the test with the thing it tested.** `crateLabel`, `moduleLabel` and
  `TestPinDirectoriesAreExcluded` all went, and keeping any of them would have
  meant keeping the mechanism they described alive to be tested.
- **A removal needs a message, not an absence.** Three config keys were rejected
  by name. A repository that set one stated something real, and ignoring it
  would leave it scanning something other than what its author wrote while every
  run reported a pass.
- **The same `cd x && cmd` trap from step 6 fired twice more**, each time
  silently skipping the command after a failed `cd` while the shell reported
  success from a later statement in the same line. Use absolute paths, and check
  that an edit landed rather than that a command exited 0.

## What step 7 left in place, that you will meet

- **`lydite scan` refuses a repository declaring no components**, exit 1. If
  step 8's workflow runs scan anywhere, a consumer without a declaration now
  fails loudly rather than silently scanning nothing.
- **`toolchain.Envs` is per component**, keyed by name, and `runComponents`
  takes it. A planner that splits components across processes has to resolve
  toolchains in each of them; they are cheap when shared and correct when not.
- **`.lydite/config.yml` no longer exists in this repository.** Everything it
  said is now either the default or stated in `.lydite/components.yml`.
- **`ci-end2end.yml` scans the proving ground** and asserts the units covered
  rather than the verdict, because the proving ground carries real findings on
  purpose. Step 8's assertions should follow that shape: assert what ran, not
  that everything was green.
- Four issues are still open and touch this work: **#47** (`lydite/actions`
  still invokes the removed `lydite coverage`, and should pass
  `GITHUB_BASE_REF`), **#48** (assert the coverage gate end to end against the
  proving ground), **#49** (the write-token question below) and **#55** (the
  Rust toolchain probe asks cargo its version rather than rustup what is
  installed).

### The security question, still open

Gating coverage means running the repository's tests *and* writing to a branch.
Step 6 answered it by recording after merge, on the default branch, with pull
requests gating read-only. Step 8 adds a job that merges shard reports and is
the natural place for someone to attach a write. Expect the question again, and
expect the same answer to apply: the merging job runs the branch's data, so a
branch can fabricate what a recording job would commit. See
[#49](https://github.com/lydite/lydite/issues/49).

## Out of scope

All of mutation (step 9). The `lydite/actions` cutover, which is a change in
that repository — though #47 must land there before v0.2.0 reaches any consumer,
since the action still invokes the removed `lydite coverage`.

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
- Run it against a real repository, not a described one. Clone
  [`lydite/proving-ground`](https://github.com/lydite/proving-ground) and drive
  the commands against it; a hand-written report fixture agrees with whatever
  the code does.
- Run it with something missing. A path that only executes when the machine is
  *not* already set up is one a development machine never reaches.
- Wire new code to a caller early. A package nothing imports can hold a defect
  through a clean build, a clean lint and a passing suite.
- Run `/code-review` before proposing the PR, and again after acting on it.
  Keep going while rounds return findings that change behaviour. Stop when what
  comes back is wording and test robustness, and say plainly that the trend is
  the reason rather than claiming the work is clean.
- Prefer one shared implementation over two that agree today.
- Do the unambiguous work first, and raise a genuine fork in the road as a
  question at the point it actually blocks something.

## Finally

Update `.agents/plans/component-platform.md` to mark step 8 done, and write
`.agents/plans/pr9-mutation.md` as the prompt for the next session, in the same
shape as this one: mutation for all three languages, built on the runner's
three variants and the coverage measurement that already exists.
