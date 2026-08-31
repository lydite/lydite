# Session prompt — step 2: the orphan-file gate

A declared component list fails open. Nothing breaks when it goes stale, so new
code is simply never tested and the build stays green. The orphan gate closes
that, and it is the last thing standing between the component model and being
trusted as the record of what gets tested.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/acequia-flash-flood`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

**The worktree is already on `feat/orphan-gate`, branched from the merged
main** (`3ff8ff8`, PR #39). Confirm with `git status` and `git log --oneline -1`
before doing anything; if it is not, `git fetch origin main` and
`git switch -c feat/orphan-gate origin/main`.

Step 1 is merged. It carries `internal/component`, `internal/runner`,
`internal/compose`, `internal/nodedeps`, `internal/cargotool`,
`internal/download`, `lydite test` and the `.lydite/` layout.

**First commit: correct the plan.** `.agents/plans/component-platform.md` still
says step 1 is "in review — #39" and the row order is deliberately shuffled.
Mark steps 1 and 3 done and put the rows back in numeric order, keeping the
paragraph that explains why 3 was folded into 1.

## Read before planning

- `docs/adr/0016-components-and-lydite-run-tests.md` — the specification. Every
  decision below is argued there. Do not re-litigate them.
- `AGENTS.md` — the Components section, the Services section, and the layout,
  conventions and comment rule.
- `CONTEXT.md` — the `Component` and `Gate` entries, and `Proving ground`.
  Terminology is policed in this repo.
- `.agents/plans/component-platform.md` — the rolling plan, what step 1 left
  for later, and the known traps.
- `cli/internal/component/` — what a component declares and what `Load`
  already validates.

## The task

Every source file must fall under some component's `dir` or an explicit
exclude. A file under neither is an orphan, and the author clears it by
declaring a component or excluding the path — a **Gate** in CONTEXT.md's sense,
not a referral.

Settled by ADR 0016, do not reopen:

- **It needs no per-language knowledge.** It is a path question, which is why
  it replaces `internal/detect` rather than depending on it. It catches a whole
  undeclared directory, including one with no manifest yet — the case detection
  cannot see at all.
- **It does not catch a forgotten `depends_on` edge**, because both components
  exist. Pushes to the default branch running every component is what covers
  that, at step 5.

Yours to settle in the challenge interview, with a recommendation for each:

- **Where excludes live** — a key in `.lydite/components.yml` or in
  `.lydite/config.yml`. Note that everything under `.lydite/` is already a
  referral disqualifier, so either is visible in review.
- **What counts as a source file.** A generated file, a fixture, a vendored
  tree and a top-level `README.md` are all "not an orphan" for different
  reasons. A gate that fires on ordinary work is one that gets switched off,
  and a gate that never fires is one nobody notices is broken.
- **Which command reports it.** `lydite test` already reads the declaration,
  but a gate that only runs when a component runs is one a component-free
  repository never sees.
- **Whether it runs on the default branch too.** `coverage.floor` is the
  precedent: an absolute standard with no baseline reads the same on main as on
  a pull request.

## Validate it against the proving ground

`scripts/seed.ts` in `lydite/proving-ground` **is meant to be an orphan** and
that repository's README says so. It carries no exclude on purpose, and the
tempting fixes are both wrong: widening the `web` component's `dir` does not
work because `scripts/` is outside the npm workspace, and declaring a component
for it does not work because nothing tests it.

So the gate is correct only if it **fails** on that repository today. Whatever
`ci-end2end.yml`'s `proving ground` job ends up asserting, a green run there
without an exclude means the gate is not working. Decide deliberately whether
that job asserts the failure, or whether the proving ground gains the exclude
and the assertion moves to a fixture — and say which, and why, in the PR.
**Editing CI requires asking the user first.**

## Out of scope

The parallel scheduler and port locks (step 4) — `internal/compose` already
reads each stack's published host ports, so that step consumes them rather than
adding the parse. Also affected selection, `lydite matrix`, rewiring `scan` or
`coverage` onto components, deleting `internal/detect`, removing
`coverage.source`, and all of mutation.

## Open questions step 1 left, yours only if you touch them

- **`typescript.install` and `setup` overlap.** The first is repository-wide in
  `.lydite/config.yml`, the second is per component, and a JavaScript component
  can now express its install either way. Decide which is the one place, or say
  plainly why both survive.
- **Toolchains resolve per ecosystem, not per component.** Written up in the
  plan under its own heading. Node is the real gap — one runtime at the highest
  `engines.node` across every package. It belongs with step 7; do not start it
  here, but do not let this PR make it worse.

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
- **Never hand-edit a generated file.** `.github/dependabot.yml` and the
  workflows are rendered from `.gt-repo.yaml`; edit the source and run
  `gt repo sync`, then `gt repo check`. Editing the output makes `governance`
  fail, which is how it was caught in #39.
- Before proposing a PR: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `cli/`, all clean. Also `lydite scan --dir .`
  — gosec and semgrep gate this repository — and `gt repo check`.
- Ask before editing CI.

## Working mode

- Verify a claim before making it. #39 shipped a path-doubling bug that every
  local run hid, because every local run used an absolute `--dir` and CI does
  not. When a fix is for a failure you have seen, reproduce that failure's
  exact shape first, and check the new test fails without the fix.
- Prefer one shared implementation over two that agree today. `internal/nodedeps`,
  `internal/cargotool` and `internal/download` all exist for that reason, and
  the last of them holds a path-traversal guard that must never be copied.
- Do the unambiguous work first, and raise a genuine fork in the road as a
  question at the point it actually blocks something.

## Finally

Update `.agents/plans/component-platform.md` to mark step 2 done, and write
`.agents/plans/pr4-scheduler.md` as the prompt for the next session, in the
same shape as this one: the resource-constrained queue, the port locks built on
`compose.Stack.HostPorts`, and the local driver — which is the code path CI
never exercises.
