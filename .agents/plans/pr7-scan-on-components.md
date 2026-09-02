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

Step 6 took **N review rounds and produced N findings** — fill this in from the
actual run before starting, and read the trend rather than the total.

What bit, and will bite again:

- **A rejection that silently stops rejecting.** The first attempt at refusing a
  removed config key decoded the file into a shadow struct with `*yaml.Node`
  fields. yaml.v3 cannot unmarshal into that type, so every check errored — and
  the error was swallowed on the grounds that the caller reports parse failures.
  Result: the rejection accepted everything, and its own test was what caught it.
  A guard whose failure path is "return nil" needs a test that fires it.
- **Verify a format claim against the tool, not against its documentation.** An
  lcov's `LF`/`LH` records match cargo-llvm-cov's JSON totals exactly; a tally of
  its `DA` lines does not — 57 against 55 on the proving ground's three-crate
  workspace, because a line carrying two records is one line to `LF` and two to a
  tally. Both readings look correct from the format description.
- **Wire it to a caller and run it against a real repository.** Every defect that
  mattered in step 6 surfaced that way rather than from a test: a diff streaming
  into the middle of the report, git's commit line landing in the same place, and
  the fact that the proving ground's `web` workspace declares no coverage
  provider at all.
- **`cd ..` from `cli/` is the worktree; `cd ..` from the worktree is the bare
  repo.** A `git add -A && git commit` in the second one commits into `.bare` and
  moves its HEAD. Use absolute paths.

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
