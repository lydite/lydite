# Session prompt — step 0: the proving ground

Build `lydite/proving-ground`, the polyglot repository the component platform is
validated against. Tracked as lydite/lydite#38.

Runs in a **fresh worktree created from main**, in parallel with the PR 1
session. The two do not share files.

## Setup

You are in a gt-powered bare repo. The root `.envrc` scopes `GH_TOKEN` to gh
user `pedromvgomes` — read it and use that user for every `gh` command.

    git fetch origin main
    git switch -c chore/proving-ground origin/main

Most of this session's output is a **new repository**, not a branch of
lydite/lydite. Read `gh issue view 38` in full before planning.

## Read first

- `gh issue view 38` — the specification for this repository.
- `docs/adr/0016-components-and-lydite-run-tests.md` — what a component is, and
  why the shapes below matter.
- `CONTEXT.md` — the `Component` entry.
- `docs/adr/0010-repo-topology.md` — its "Two repos, not three" section, which
  this contradicts on its face and needs a reconciling line.

## The task

Create a public repository `lydite/proving-ground` containing a Rust CLI, a Go
API and a React app, arranged so that lydite's platform can be exercised
against it.

**It must be awkward, not tidy.** A clean repository validates the happy path;
every shape below changed the design during the wardnet review and must be
present:

| shape | proves |
|---|---|
| a Cargo **workspace** of several crates | a component is a build unit, not a deployable |
| a JS workspace, 2+ packages, one `node_modules` | the same, on the node side |
| **two** separate Go modules | the case where splitting *is* correct |
| the Go API emits an OpenAPI spec the React app consumes | a cross-language `depends_on` no tool can derive |
| one component needing Postgres via compose | compose delegation, `wait: healthy`, teardown |
| two components publishing the **same** host port | the local scheduler must serialise them |
| a `Makefile` outside every component, invalidating one | per-component `watch:` |
| a source file under no component | the orphan gate fires |

**And deliberately weak tests**, because mutation is only observable when
mutants survive:

- a test that calls a function and asserts nothing — every mutant it covers survives
- an uncovered function — its mutants are skipped, not reported as survivors
- a function with no mutable sites — the honest "no signal" case
- a genuinely equivalent mutant — unkillable, and must be acknowledgeable

Keep each app small. This is a fixture, not a demo — every line should exist to
exercise something named above.

## Also in scope

1. **A `.lydite/components.yml`** declaring the components, following the schema
   in ADR 0016. It will not be consumed until PR 1 lands; write it anyway, as
   the reference a consumer copies, and as the first real test of whether the
   schema survives contact with a repository.
2. **A line in `docs/adr/0010-repo-topology.md`** (in lydite/lydite, on the
   branch above) reconciling a third repository with "Two repos, not three":
   the argument there is that `cli/`, `web/` and `worker/` ship as one artifact
   from one tag; a proving ground ships nothing and is never released.
3. **Do NOT wire `ci-end2end.yml`** to run against it yet. That belongs with
   PR 1, which is what will have something to run.

## Non-negotiable workflow rules

- Before calling `ExitPlanMode`, invoke the `challenge` skill and complete its
  interview. A plan that has not been challenged is incomplete.
- Commits and PR bodies must NEVER contain `Co-Authored-By` lines, a
  `Claude-Session` trailer, or any `claude.ai/code/session_...` URL.
- Use `Closes #N` on PRs, not `Refs`.
- Never merge a PR unless explicitly told to.
- Comments describe the code as it is, never the change that produced it. No
  "used to", "previously", "no longer", no roadmap talk, no ticket numbers.
  ADRs are the only exception.
- The new repository's own code must build and its tests must run — the weak
  tests above are deliberately weak, not broken.

## Finally

Update `.agents/plans/component-platform.md` to mark step 0 done, and say in
the PR body which of the shapes above are present and which you could not build.
