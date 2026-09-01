# Session prompt — step 5: affected selection

Every declared component runs on every invocation today. ADR 0016 has a pull
request run only the components a change could have broken, and the default
branch run all of them — which is what makes selection an optimisation with a
bounded failure rather than a correctness mechanism.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

    git fetch origin main
    git switch -c feat/affected-selection origin/main

**Verify steps 1, 2 and 4 are merged first.** Step 1 (#39) carries
`internal/component`, `internal/runner`, `internal/compose` and `lydite test`.
Step 2 (#42) carries `internal/orphan`, `internal/pathmatch` and the `excludes`
key. Step 4 (#44) carries `internal/scheduler`, `--concurrency`, the `schedule`
row and the shard model in
[ADR 0017](../../docs/adr/0017-shards-the-scheduler-and-the-planner.md). If any
is missing, stop and say so.

**First commit: correct the plan.** `.agents/plans/component-platform.md`
should say step 5 is in progress and name this file as its prompt.

## Read before planning

- `docs/adr/0016-components-and-lydite-run-tests.md` — "Affected selection, with
  the default branch as the backstop". Do not re-litigate it.
- `docs/adr/0017-shards-the-scheduler-and-the-planner.md` — selection feeds the
  planner at step 8; a shard is grouped from what selection returned.
- `AGENTS.md` — the Components section, the output grammar, and the comment rule.
- `CONTEXT.md` — `Component`, `Shard`, `Gate`, `Orphan`.
- `cli/internal/gitstate/` — `BaseSHA` already resolves the merge-base every
  other diff-reading command uses.
- `cli/internal/coverage/` — `ChangedLines` already parses a unified diff, and
  `--relative` is already load-bearing there.
- `cli/internal/pathmatch/` — the anchored matcher `watch` patterns are written in.
- `cli/cmd/lydite/test.go` — `file.Select`, which is where selection lands.

## The task

Run only the components a change could have broken, on a pull request.

Settled by ADR 0016, do not reopen:

- **A component runs when the diff touches its directory, one of its `watch`
  paths, a component it `depends_on` transitively, or a global invalidator.**
- **Pushes to the default branch run every component.** A forgotten edge means
  a dependent is not run on the change that broke it, and no gate catches it —
  both components exist and no file is orphaned. Running everything at merge is
  what turns that from never-caught into caught-one-merge-late.
- **`depends_on` is transitive for selection.** This is the one thing it is
  for, and step 4 settled that it means nothing to the scheduler: it does not
  order execution, it decides what runs at all.

Yours to settle in the challenge interview, with a recommendation for each:

- **What exactly is a global invalidator.** ADR 0016 names "a workspace
  manifest, a lockfile, a toolchain file, or `.lydite/` itself". Each of those
  is a family rather than a path, and the list is the whole safety of this
  feature — every file that belongs on it and is missing is a change that runs
  nothing. Decide whether it is a fixed built-in set, a declarable one, or both,
  and what happens to a file that matches nothing at all.
- **Whether an unrecognised path is selecting or non-selecting.** A file under
  no component, on no `watch` list and matching no invalidator, is either
  harmless or the case the built-in list has not learned yet. Note the orphan
  gate already refuses to let a *source* file sit under no component, which
  bounds this question but does not answer it for everything else.
- **How selection is triggered.** A flag, an inferred base, or the presence of a
  merge-base. `gitstate.BaseSHA` already exists; what must not happen is a
  default that silently narrows a local `lydite test` into running nothing.
- **What selection hands the scheduler.** `runComponents` builds one
  `scheduler.Item` per selected component through `itemFor`, and the `schedule`
  row's denominator is every component the run was given. Decide whether a
  deselected component is absent from the report or present and marked, and
  note that the second is what lets a reader tell "not affected" from "not
  declared".
- **How a run that selected nothing reports.** This is the whole risk (see
  below) and the answer is not "exit 0 quietly".
- **What a rename, a deletion, and a new directory do.** A component whose `dir`
  no longer exists, and a diff that adds a directory no component claims, are
  both real and neither is a path under an existing component.

## The failure class this step is most exposed to

Steps 2 and 4 both kept producing one shape: **a check that could not run
reading as a check that passed.** Selection is the most dangerous place in the
whole platform for it, because narrowing what runs is its *purpose* — a bug and
the intended behaviour differ only in whether the narrowing was correct, and
both produce a green run in less time, which is what everyone wants to see.

- A `watch` pattern that matches nothing looks exactly like a component nothing
  invalidated. Step 2 hit this with excludes and named them on stderr; the same
  answer probably does not suffice here, because an exclude covering nothing is
  tidying and a `watch` covering nothing is a component that will not run when
  its input changes.
- A merge-base that cannot be resolved must never degrade to "nothing changed".
  `lydite scan --diff-base auto` already refuses rather than silently widening;
  the mirror of that here is refusing rather than silently narrowing.
- "0 of 4 components affected" must be visibly distinct from "4 of 4 passed",
  in the text grammar and in `--json`. A `schedule` row already reports what ran;
  whatever reports what was *skipped* has to make the skipping legible, and a
  consumer must be able to tell the two apart without parsing prose.

**Assert that the right things were skipped, not that the run was green.** A
selection that returns everything passes every test about correctness and
delivers none of the value; a selection that returns nothing passes every test
about speed and none about correctness. Both need to fail a test.

### What step 4 learned about it, at a cost of seven review rounds

Step 4 went through seven rounds of `/code-review` and every round found
something. None of it was caught by `go build`, `go test -race` or
`golangci-lint`, all of which were clean the entire time. Read this before
assuming a green gate means anything here.

- **Fixing the shape in one place does not fix it.** "An interrupted run reads
  as a pass" was found and fixed four times over: first in the process exit
  code, then again in the `--json` verdict — which is the surface every
  consumer is told to read — then again in `scan` and `coverage`, which a
  signal handler installed in `main.go` had silently changed, then again for
  suites killed mid-flight, whose non-zero exits read as test failures.
  Each fix was correct and each covered one path. **When you fix an instance,
  enumerate the other layers the same question is asked at.**
- **Two of the tests written for that step were vacuous, and both looked
  fine.** One asserted that a port-conflicting pair was serialised and passed
  with the port lock removed entirely. One replaced `PATH` by *prepending* a
  temp directory, so the runtime it meant to hide stayed resolvable. Neither
  was found by reading; both were found by deleting the code under test and
  watching the test pass anyway.
- **So: injecting the defect is the only thing that establishes a test.** For
  every assertion that matters, break the thing on purpose, confirm the test
  fails, and restore. Confirm it reports a *failure* — one injection here
  printed "no tests to run", because an earlier edit had silently deleted the
  test it was checking.
- **Verify a claim against the real repository, not against the prompt.** The
  CI assertion for step 4 named the two colliding components `go/api` and
  `rust`, which are their *directories*; the components are `api` and `tally`.
  A constant naming something that does not exist matches nothing, and an
  assertion that matches nothing passes. Only running it exposed that.
- **A comment that claims a property is a claim to check.** One comment stated
  that `signal.NotifyContext` restores the default disposition after the first
  signal. It does not — reading `$GOROOT/src/os/signal/signal.go` showed the
  handler stays registered — so the safety it promised did not exist.
- **A review finding is not automatically right.** One round reported that an
  interactive Ctrl-C still leaks containers, because the signal reaches the
  whole process group. It does not: a group signal only reaches processes alive
  at that instant, and the teardown child is spawned afterwards. Reproducing it
  took ten minutes and saved a pointless fix. Check before you act.
- **A timing-dependent test is a defect you are choosing to ship.** Two of step
  4's tests had fifty-millisecond margins and were rewritten to be driven by
  synchronisation instead. Where an assertion genuinely cannot avoid a clock,
  make the *failure* the thing that times out, never the pass.

## Validate it against the proving ground

`lydite/proving-ground` was built with real edges for this:

- `tally` watches `Makefile` and `VERSION`, which sit under no component, and
  `tally-cli` embeds `VERSION`.
- `sdk` and `web` both `depends_on: [api]` and both watch `docs/openapi.json` —
  the generated-client edge no tool can see.
- `go/api` and `go/sdk` are separate modules; `rust` is one workspace of three
  crates; `web` is one npm workspace of two packages.

So there are diffs with known correct answers: a change to `VERSION` selects
`tally` alone, a change to `docs/openapi.json` selects `sdk` and `web` and
not `api`, which does not watch it, a change under
`go/api/` selects `api`, `sdk` and `web` through the dependency edge, and a
change to a lockfile selects everything.

Extend `.github/assert-proving-ground.py` rather than adding a second assertion
mechanism beside it. It already holds the orphan gate to firing on
`scripts/seed.ts` and staying silent on the excluded `generated/client.ts`, and
the scheduler to having reached two components at once and serialised `tally`
and `api` on 5432. **Editing CI requires asking the user first.**

## Out of scope

Coverage onto components (step 6), scan onto components and deleting
`internal/detect` (step 7), `lydite test plan`/`lydite test merge` and the
reusable workflow (step 8), and all of mutation (step 9). Selection produces the
component list the planner at step 8 groups into shards; it does not group
anything itself.

## Open questions earlier steps left, yours only if you touch them

- **`typescript.install` and `setup` overlap.** The first is repository-wide in
  `.lydite/config.yml`, the second is per component. Decide which is the one
  place, or say plainly why both survive.
- **Toolchains resolve per ecosystem, not per component.** Node is the real gap
  — one runtime at the highest `engines.node` across every package. It belongs
  with step 7; do not start it here, but do not let this PR make it worse.
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

- Verify a claim before making it, against the repository rather than against
  this file. See the section above for how step 4 learned that.
- Run it against a real diff, not a described one. `git diff` output over a
  branch you actually made is the input; a hand-written path list is a fixture
  that agrees with whatever the code does.
- Run `/code-review` before proposing the PR, and again after acting on it.
  Keep going while rounds still return findings that change behaviour — step 4
  needed seven, and rounds five and six each found a place the previous fix had
  not reached. Stop when what comes back is wording and test robustness rather
  than behaviour, and say plainly that the trend is the reason rather than
  claiming the work is clean.
- Prefer one shared implementation over two that agree today. `internal/pathmatch`,
  `internal/gitstate` and `internal/scheduler` all exist for that reason, and
  selection touches all three.
- Do the unambiguous work first, and raise a genuine fork in the road as a
  question at the point it actually blocks something.

## Finally

Update `.agents/plans/component-platform.md` to mark step 5 done, and write
`.agents/plans/pr6-coverage-on-components.md` as the prompt for the next
session, in the same shape as this one: `#36`'s `-coverpkg` reset landing in the
same change as coverage moving onto components, `cargo-llvm-cov` finally being
installed, and `coverage.source` being removed once the component is the unit
coverage is measured over.
