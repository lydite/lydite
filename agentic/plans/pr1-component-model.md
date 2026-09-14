# Session prompt — step 1: the component model, runners, and `lydite test`

The walking skeleton of the component platform: a repository declares
components, and lydite can run one component's tests.

Runs in the **existing worktree**, in parallel with the step 0 session building
`lydite/proving-ground` (lydite/lydite#38).

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/acequia-flash-flood`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

    git fetch origin main
    git switch -c feat/component-model origin/main

**Verify PR #37 is merged first** (`gh pr view 37 --json state,mergedAt`). It
carries ADR 0016 and the `Component` glossary entry, which specify this work.
If it is not merged, stop and say so.

## Read before planning

- `docs/adr/0016-components-and-lydite-run-tests.md` — the specification. Every
  decision below is argued there. Do not re-litigate them.
- `CONTEXT.md` — the `Component` entry. Terminology is policed in this repo.
- `AGENTS.md` — layout, conventions, boundaries, the comment rule.
- `.agents/plans/component-platform.md` — the rolling plan and its known traps.
- Issue #36 (`-coverpkg`), and ADR 0006 (pins).

## The task

### 1. `.lydite/` layout

- `.lydite.yml` → `.lydite/config.yml`
- `.lydite.exemptions.yml` → `.lydite/exemptions.yml`
- new: `.lydite/components.yml`

Touches `internal/config` and `internal/referral` (`referral.FileName`). Update
lydite's own config and every fixture. There are no consumers before v1, so no
back-compat shim and no deprecation window.

**Do NOT remove `coverage.source` in this PR.** ADR 0016 removes it, but that
happens when coverage moves onto components at step 6, and lydite's own CI
depends on `source: report` today. Carry the key across unchanged.

### 2. `internal/component`

Parse and validate `.lydite/components.yml`:

```yaml
components:
  - name: cli                      # unique; names the matrix job and report rows
    dir: cli                       # component root, repo-relative
    runner: go-test                # implies the language
    args: ["./..."]
    watch: ["Makefile", "VERSION"] # paths outside dir that invalidate this component
    depends_on: [sdk]              # declared; not derivable across languages
    env:
      FOO: bar
    compose:
      file: ./docker/compose.yaml  # default: component root
      up: [db]                     # default: every service in the file
      wait: healthy                # healthy | started | none
    setup: ["make migrate"]
    teardown: ["rm -rf ./data"]
    mutation: false                # opt-out, default true
```

Settled rules:

- **`lang` is derived from `runner`, never declared.** `cargo-nextest` can only
  be Rust.
- **Unknown keys are rejected**, as `referral.Parse` and
  `config.validateLinter` already do. A silently-ignored key means a component
  configured differently from what its author wrote.
- **A component is the unit its build tool treats as a whole** — a Cargo
  workspace, a Go module, a JS workspace. Not a deployable. Enforce nothing,
  but say it in the package doc; it is the rule most likely to be got wrong.
- Validate: unique names, `dir` exists and is inside the repository,
  `depends_on` resolves to declared components, no dependency cycles.

### 3. `internal/runner`

A registry mapping a runner name to three invocations of the same suite:

| variant | why it exists |
|---|---|
| plain | the fast path, for mutants |
| instrumented | the coverage gate, and mutation's baseline |
| build-only | tells an *unviable* mutant from a *killed* one |

All three languages ship here. Go first because it is the only one lydite can
dogfood, but cargo and node runners are part of this deliverable:

- `go-test` — plain `go test`; instrumented adds `-coverprofile` **and
  `-coverpkg=./...`** (see #36); build-only is `go build ./...`
- `cargo-nextest` and `cargo-llvm-cov-nextest` — instrumented *replaces the
  runner* with `cargo llvm-cov`; it is not an added flag
- `vitest` (and `jest` if it is cheap) — instrumented adds `--coverage`

Plus a raw `command:` escape hatch, which opts out of derived variants and
requires the component to declare its own coverage invocation.

Collect JUnit where the runner emits it — the quality-history ledger (#26)
records test counts, and both reference pipelines already produce it.

Unit tests assert the constructed invocation and never execute a foreign
toolchain, matching `internal/rust` and `internal/typescript`.

### 4. `lydite test`

Runs components' tests and reports through `internal/ui` like every other
command. Flags: `--dir`, `--component` (repeatable, default all), `--json`,
`--no-color`. Sequential — the scheduler is step 4. No compose and no
setup/teardown yet: a component declaring `compose` must **fail with a clear
message**, never silently run tests without its services.

### 5. Dogfood

`.lydite/components.yml` declaring lydite's single Go component at `cli/`.

## Gating on the proving ground

Two of the three runners cannot be exercised by this repository. Before opening
the PR, check whether `lydite/proving-ground` (#38) exists:

- **If it does**: add a job to `.github/workflows/ci-end2end.yml` that checks it
  out and runs `lydite test` against its Rust and TypeScript components,
  asserting the verdict. **Editing CI requires asking the user first.**
- **If it does not**: open the PR as a draft, say plainly in the body that the
  cargo and vitest runners are asserted but never executed, and do not mark it
  ready until that job exists.

## Out of scope

Services and compose, the parallel scheduler and port locks, affected
selection, the orphan-file gate, `lydite matrix`, rewiring `scan` or `coverage`
onto components, deleting `internal/detect`, and all of mutation.

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

Update `.agents/plans/component-platform.md` to mark step 1 done, and write
`.agents/plans/pr2-orphan-gate.md` as the prompt for the next session, in the
same shape as this one: the orphan-file gate, then `internal/compose`.
