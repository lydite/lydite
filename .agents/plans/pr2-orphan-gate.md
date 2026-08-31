# Session prompt — step 2: the orphan-file gate, then `internal/compose`

A declared component list fails open: nothing breaks when it goes stale, so new
code is simply never tested and the build stays green. The orphan gate closes
that. `internal/compose` then gives a component the services its suite needs.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

    git fetch origin main
    git switch -c feat/orphan-gate origin/main

**Verify step 1 is merged first** — it carries `internal/component`,
`internal/runner`, `lydite test` and the `.lydite/` layout everything below
builds on. If it is not merged, stop and say so.

## Read before planning

- `docs/adr/0016-components-and-lydite-run-tests.md` — the specification. Every
  decision below is argued there. Do not re-litigate them.
- `AGENTS.md` — the Components section, plus layout, conventions and the
  comment rule.
- `CONTEXT.md` — the `Component` and `Gate` entries. Terminology is policed in
  this repo.
- `.agents/plans/component-platform.md` — the rolling plan and its known traps.
- `cli/internal/component/` and `cli/internal/runner/` — what a component
  already declares, including the `compose` block that currently only fails.

## The task

### 1. The orphan-file gate

Every source file must fall under some component's `dir` or an explicit
exclude. A file under neither is an orphan, and the gate is cleared by
declaring a component or excluding the path — it is a **Gate** in CONTEXT.md's
sense, not a referral.

Settled by ADR 0016:

- **It needs no per-language knowledge.** It is a path question, which is why
  it replaces `internal/detect` rather than depending on it. It catches a whole
  undeclared directory, including one with no manifest yet — the case detection
  cannot see at all.
- **It does not catch a forgotten `depends_on` edge**, because both components
  exist. That is what pushes to the default branch running every component are
  for, at step 5.

Open, and yours to settle in the challenge interview:

- Where excludes live: a key in `.lydite/components.yml` or in
  `.lydite/config.yml`. Note that everything under `.lydite/` is already a
  referral disqualifier, so either choice is visible in review.
- What counts as a source file. A generated file, a fixture, a vendored tree
  and a top-level `README.md` are all "not orphans" for different reasons, and
  a gate that fires on ordinary work is one that gets switched off.
- Which command reports it. `lydite test` sees the declaration already; a
  gate that only runs when a component runs is a gate a component-free
  repository never sees.

### 2. `internal/compose`

A component's services are a compose file: lydite owns no service schema, and
must not grow one. Images, ports, environment and healthchecks are compose's
job already.

- **Probe for a runtime; never hard-code one.** podman on a laptop, docker on a
  runner — the implementation is a property of the machine, not the repository.
  Name which one was chosen, on stderr.
- **`wait: healthy` requires a declared healthcheck, and refuses without one**
  rather than degrading to `started`. A suite racing a database that is not yet
  listening is the flakiest thing a pipeline can contain, and the failure is
  attributed to the test rather than to the wait.
- **Teardown runs even when the run fails.** Leaked containers poison the next
  local run.
- Read `ports:` while the file is open. Step 4's scheduler locks on published
  host ports, and nothing else in the file has to be parsed twice.
- `setup` and `teardown` commands cover what is not a container — migrations,
  fixtures, cleanup.

`--keep-services` is an invocation choice, not a repository fact, so it is a
flag and never a key in the file.

Wire both into `lydite test`, replacing the failing rows a component declaring
`compose`, `setup` or `teardown` gets today.

## Out of scope

The parallel scheduler and port locks (step 4), affected selection, `lydite
matrix`, rewiring `scan` or `coverage` onto components, deleting
`internal/detect`, removing `coverage.source`, and all of mutation.

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
- Before proposing a PR: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `cli/`, all clean. Also `lydite scan --dir .`
  — gosec and semgrep gate this repository.
- Ask before editing CI.

## Finally

Update `.agents/plans/component-platform.md` to mark step 2 done, and write
`.agents/plans/pr3-scheduler.md` as the prompt for the next session, in the
same shape as this one: the resource-constrained queue, port locks, and the
local driver.
