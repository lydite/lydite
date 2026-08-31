# Session prompt — step 4: the scheduler

Components run one after another today. ADR 0016 has them run in parallel,
except that a component holds a lock on each host port its compose services
publish. `internal/compose` already reads those ports and nothing consumes
them; this step is what consumes them.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

    git fetch origin main
    git switch -c feat/scheduler origin/main

**Verify steps 1 and 2 are merged first.** Step 1 carries `internal/component`,
`internal/runner`, `internal/compose`, `internal/nodedeps`,
`internal/cargotool`, `internal/download` and `lydite test`. Step 2 carries
`internal/orphan` and `internal/pathmatch`. If either is missing, stop and say
so.

**First commit: correct the plan.** `.agents/plans/component-platform.md`
should say step 4 is in progress and name this file as its prompt.

## Read before planning

- `docs/adr/0016-components-and-lydite-run-tests.md` — the specification, and
  in particular "Scheduling is resource-constrained". Do not re-litigate it.
- `AGENTS.md` — the Components and Services sections, the output grammar, and
  the comment rule.
- `CONTEXT.md` — `Component`, `Gate` and `Proving ground`.
- `.agents/plans/component-platform.md` — the rolling plan and its known traps.
- `cli/internal/compose/` — `Stack.HostPorts` and what the probe already does.
- `cli/cmd/lydite/test.go` — `runComponent`, and the sequential loop that
  becomes the queue.

## The task

Run components concurrently under a resource-constrained queue.

Settled by ADR 0016, do not reopen:

- **The lock is keyed on published host ports, not service names.** The
  conflict is physical: two components each calling a service `db` on
  different ports do not conflict and must not be serialised, and a `db` and a
  `postgres` on the same port do. A name-keyed lock would miss the second and
  fail mid-run on a bind error — and the proving ground is built to catch
  exactly that, with `go/api/compose.yaml` and `rust/compose.yaml` both
  publishing 5432 under differently-named services.
- **Sharing one running service between concurrent components is rejected.**
  Two suites against one database truncate each other's tables, and the
  failure is non-deterministic and reads as a bad test.
- **This constraint binds locally only.** In CI each component is a separate
  job on a separate machine and nothing contends.

Yours to settle in the challenge interview, with a recommendation for each:

- **What bounds concurrency besides ports.** NPROC, a flag, something derived
  from the components themselves — and whether a component with no services is
  bounded at all.
- **What `depends_on` means to the scheduler.** It is declared for affected
  selection at step 5; whether it also orders execution here is a separate
  question, and answering "yes" quietly turns an invalidation edge into a
  build-order edge that nothing validates as one.
- **How a failure interacts with work already running.** Fail fast and cancel,
  or run everything and report every row. `lydite test` emits one report with
  one row per component today, and a cancelled component's row has to say
  something honest.
- **Whether the report stays deterministic.** Rows are in declaration order
  now, and a reader diffing two runs depends on that. Completion order is the
  tempting default and is the one that makes a report unreadable.

## The local driver is the code path CI never exercises

ADR 0016 says so outright, and it is the reason this step is riskier than it
looks: in CI every component is its own job, so nothing here runs. A unit test
over a fake clock and fake stacks is most of the answer, and the rest is
running `lydite test` locally against `lydite/proving-ground`, whose two
port-colliding components exist for this.

`--keep-services` interacts with all of it. A component that leaves its stack
up still holds its ports, and the next run's scheduler has to see that.

## Validate it against the proving ground

`go/api` and `rust` both publish host port 5432, under services named `db` and
`postgres`. A scheduler that runs them concurrently fails on a bind error; one
that serialises them on the *name* passes this repository while being wrong,
which is why the names differ. A run that is green without ever having
serialised those two proves nothing — assert the ordering, not the exit code.

Its `ci-end2end.yml` job already asserts a verdict rather than an exit code
(see `.github/assert-proving-ground.py`); extend that rather than adding a
second assertion mechanism beside it. **Editing CI requires asking the user
first.**

## Out of scope

Affected selection and the full run on the default branch (step 5), coverage
onto components (step 6), scan onto components and deleting `internal/detect`
(step 7), `lydite matrix` and the reusable workflow (step 8), and all of
mutation (step 9).

## Open questions earlier steps left, yours only if you touch them

- **`typescript.install` and `setup` overlap.** The first is repository-wide in
  `.lydite/config.yml`, the second is per component. Decide which is the one
  place, or say plainly why both survive.
- **Toolchains resolve per ecosystem, not per component.** Node is the real
  gap — one runtime at the highest `engines.node` across every package. It
  belongs with step 7; do not start it here, but do not let this PR make it
  worse.
- **`cargo-llvm-cov` is still not installed**, which is what poisons a baseline
  under `coverage.source: run`. It belongs with step 6.

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
  — gosec and semgrep gate this repository — and `gt repo check`.
- Ask before editing CI.

## Working mode

- Verify a claim before making it. #39 shipped a path-doubling bug that every
  local run hid, because every local run used an absolute `--dir` and CI does
  not. When a fix is for a failure you have seen, reproduce that failure's
  exact shape first, and check the new test fails without the fix.
- A concurrency bug that passes once has not passed. `go test -race` is the
  floor, not the proof; a scheduler test that depends on real timing is one
  that goes flaky in someone else's CI six weeks from now.
- Prefer one shared implementation over two that agree today. `internal/nodedeps`,
  `internal/cargotool`, `internal/download` and `internal/pathmatch` all exist
  for that reason.
- Do the unambiguous work first, and raise a genuine fork in the road as a
  question at the point it actually blocks something.

## Finally

Update `.agents/plans/component-platform.md` to mark step 4 done, and write
`.agents/plans/pr5-affected-selection.md` as the prompt for the next session,
in the same shape as this one: the invalidation rules `watch` and `depends_on`
feed, the global invalidators, and the default branch running everything as
the backstop that makes affected selection an optimisation rather than a
correctness mechanism.
