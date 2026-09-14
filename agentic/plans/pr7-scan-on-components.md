# Session prompt — step 7: scan onto components, and `internal/detect` deleted

`lydite scan` is the last command that discovers its own units by walking for
manifests. Coverage stopped at step 6, so `internal/detect` now has two callers
left — scan, and the toolchain provisioning scan does first — and neither of
them has a reason to keep guessing what the declaration already states. This is
that move, plus the Node gap it finally makes closable.

## Setup

You are in a gt-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`.
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes` — read it and use
that user for every `gh` command. The Go module lives at `cli/`, not the repo
root; run all go and golangci-lint commands from there.

    git fetch origin main
    git switch -c feat/scan-on-components origin/main

**Verify step 6 is merged first.** It carries `coverage.Measure`, the removal of
`lydite coverage`, the `v3` per-component baseline, `gitstate.BaseBranch` and
[ADR 0019](../../docs/adr/0019-coverage-per-component-gated-by-lydite-test.md).
If it is missing, stop and say so.

**First commit: correct the plan.** `.agents/plans/component-platform.md`
should say step 7 is in progress and name this file as its prompt.

## Read before planning

- `docs/adr/0016-components-and-lydite-run-tests.md` — the component is the
  unit. Do not re-litigate it.
- [ADR 0019](../../docs/adr/0019-coverage-per-component-gated-by-lydite-test.md) —
  what "the component is the unit" turned out to mean for a gate, and the shape
  the answer took. Scan is the same move against a different surface.
- `docs/adr/0004-ensure-language-toolchains.md` — how toolchains resolve today,
  and why lydite does it rather than each caller's CI.
- `AGENTS.md` — Toolchains, Linters, Tool version pins, and the Components
  section's `lang` derivation.
- `CONTEXT.md` — `Component`, `Gate`, `Shard`.
- `cli/internal/detect/` — every function, and who calls each.
- `cli/cmd/lydite/scan.go` — `enabledEcosystems` lives here now, with one caller.

## The task

Three things, and the third is what the first two exist to make possible.

**Scan learns its units from the declaration.** A component names a runner, the
runner implies a language, and the component's `dir` says where that language's
code is. That is everything `internal/detect` was inferring. `internal/rust`,
`internal/typescript` and `internal/golang` keep their own logic; what changes is
who tells them where to look.

**`internal/detect` is deleted.** Not deprecated — deleted, with `detect.Ecosystem`,
`detect.Extensions`, `GoModuleDirs`, `RustCrateDirs`, `TSPackageDirs` and the
excludes that fed them going with it. A discovery mechanism kept "just in case"
beside a declaration is two answers to one question, and the one that rots is the
one nothing exercises.

Decide what happens to `rust.exclude` / `typescript.exclude` / `go.exclude` in
`.lydite/config.yml`. They narrowed *detection*, and detection is going. A
repository that set one is saying something real — lydite's own config excludes
the pin directories by name — so this is a removal that needs an answer, not a
deletion. `components.yml`'s `excludes` already says "no component tests this",
which is close but not identical: an excluded path is still scanned today.

**Toolchains resolve per component, not per ecosystem.** This is the gap
deferred since step 1 and it is real: `engines.node` is read from every detected
package and the highest wins, so a monorepo whose `web/` needs Node 22 and whose
`tools/` pins Node 18 gets one runtime, chosen by a rule neither package stated.
Rust already half-solves it (rustup reads `rust-toolchain.toml` from the
directory cargo runs in, so per-crate selection happens for free) and Go
delegates to `GOTOOLCHAIN`. Node is the one that has no such mechanism, and the
component is the unit that finally makes the question answerable.

## What step 6 learned, and what it cost

Step 6 took **seven review rounds and produced 35 findings**, none of them a
false positive. The counts by round were 6, 6, 4, 5, 5, 3, 6 — the rate never
converged, and the two most dangerous defects arrived in rounds 3 and 4, after
the point where a quiet round might have looked like a finish. Budget for six
rounds and read the *class* of finding rather than the count: the honest stopping
signal was the last round returning "this diagnostic is wrong" and "this
denominator counts the wrong thing" rather than "this gate reports green having
measured nothing".

Three of the findings were defects the reviewer found in code written to fix an
earlier finding. One was written an hour before it was found.

What bit, and will bite again:

- **Almost every defect that mattered came from running it, not from a test.**
  A vitest run deleting every component's log; `0/0` rendering as `0.0%`; a
  failed component carrying a stale baseline into a green row; lydite's own
  output widening its own affected selection; the default branch measuring
  everything twice. Tests confirmed each one *afterwards*. Build real
  repositories — a git fixture with an origin, a `--dir source` monorepo, an
  unwritable remote — and run the thing.
- **The two worst defects were both in code at 0% coverage, and neither was
  reachable from a unit test.** A base tree measured at the worktree root
  instead of the scan root, and a base tree's config validated with rules it
  predates. Each produced a gate that passed having compared nothing. If a
  package has a network or worktree path, drive it end to end through the
  command or accept that reviews are the only thing testing it.
- **A guard whose failure path is `return nil` needs a test that fires it.** The
  first attempt at rejecting a removed config key decoded into a shadow struct
  with `*yaml.Node` fields, which yaml.v3 cannot unmarshal into. Every check
  errored, the error was swallowed as "the caller reports parse failures", and
  the rejection accepted everything. Its own test caught it.
- **Injecting the defect is what establishes a test, and the injection has to
  compile.** One injection left an unused import; nothing built, no test ran,
  and the filtered output looked exactly like a passing injection. Read the
  build output.
- **Verify a format claim against the tool.** An lcov's `LF`/`LH` records match
  cargo-llvm-cov's JSON totals exactly; a tally of its `DA` lines does not — 57
  against 55 on the proving ground — because a line carrying two records is one
  line to `LF`. Both readings look right from the format description.
- **`cd ..` from `cli/` is the worktree; `cd ..` from the worktree is the bare
  repo.** A `git add -A && git commit` in the second one commits into `.bare`
  and moves its HEAD. Use absolute paths. Also: `cd x && cmd` silently skips
  `cmd` when the `cd` fails, which cost an edit that appeared to apply and had
  not.
- **A CI edit is code.** `working-directory: ..` in a workflow step resolves
  against the workspace, not the job's default — so it named the parent of the
  checkout, where `lydite test` reports "no components declared" and exits 0.
  The suite and the gate would both have stopped running while the job stayed
  green. Written while fixing a different finding, found by the next round.

### A security question this step opened, and did not close

Gating coverage means running the repository's tests *and* writing to a branch.
Put both in one job and a pull request's own code runs beside a token that can
push anywhere. Splitting them does not help: the measuring job runs the branch's
code whatever token it holds, so a branch can fabricate the measurement a
recording job would then commit — trading trust in the branch's token for trust
in its data, where a poisoned baseline persists and every later change gates
against it.

The answer taken was to record **after merge**, on the default branch, measuring
the tree itself. Pull requests gate read-only. See
[#49](https://github.com/lydite/lydite/issues/49), and expect the same question
wherever step 7 or 8 adds a job that writes.

## What step 6 left in place, that you will meet

- **This repository gates itself.** `.github/workflows/ci-test.yml` runs
  `lydite test --dir .. --gate-coverage` **read-only**, and
  `.github/workflows/lydite-baseline.yml` records on pushes to the default
  branch. If step 7 changes what scan measures, that gate is now the thing that
  notices — including on step 7's own pull request.
- **`enabledEcosystems` moved to `cmd/lydite/scan.go`** and has one caller. It
  is the obvious casualty of deleting `internal/detect`, and where
  `rust/typescript/go.enabled` is answered today.
- **`ensureToolchains` is called by both `scan` and `test`**, and `test` derives
  its ecosystems from the component declaration (`componentEcosystems`) rather
  than from a walk. That is the shape step 7 extends: scan should do the same,
  and then nothing reads `detect.Ecosystems` at all.
- **`internal/runner.SourceExtsFor`** already maps a language to its extensions,
  which is what `detect.Extensions` was for.
- Three issues are open and touch this work: **#47** (`lydite/actions` still
  invokes the removed `lydite coverage`, and should pass `GITHUB_BASE_REF`),
  **#48** (assert the coverage gate end to end against the proving ground) and
  **#49** (the write-token question above).

## Out of scope

`lydite test plan` / `lydite test merge` and the reusable workflow (step 8). All
of mutation (step 9). The `lydite/actions` cutover, which is a change in that
repository.

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
- Do the unambiguous work first, and raise a genuine fork in the road as a
  question at the point it actually blocks something.

## Finally

Update `.agents/plans/component-platform.md` to mark step 7 done, and write
`.agents/plans/pr8-plan-and-merge.md` as the prompt for the next session, in the
same shape as this one: `lydite test plan` emitting the shard matrix, `lydite
test merge` folding the shards' reports into one, and the reusable workflow that
makes a consumer's CI one `uses:`.
