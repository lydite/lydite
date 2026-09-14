# Session prompt — implement step 8: `lydite test plan`, `lydite test merge`

The design is settled. It was challenged decision by decision in the session that
wrote [ADR 0026](../../docs/adr/0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md),
and that ADR is the specification — **do not re-litigate it.** This prompt is the
implementation order, the traps, and the things the ADR does not say.

Closes [#61](https://github.com/lydite/lydite/issues/61) and
[#48](https://github.com/lydite/lydite/issues/48).

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`, on
branch `feat/plan-and-merge-shards`, branched from `main` at `dc9129e` (#88).
The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes`.

The Go module is at `source/cli/`; the scan root is the repository root above it,
which is where `.lydite/` lives. Every `go` and `golangci-lint` command runs from
`source/cli`; `lydite scan --dir .` and `gt repo check` run from the root.

**Already committed on the branch** (the decision record, written during the
challenge — read it first, and change it only if implementation proves it wrong):

- `docs/adr/0026-…-the-fold-decides-completeness.md` — the specification.
- `docs/adr/0025-…` — two superseding notes.
- `docs/adr/0017-…` — the `--concurrency` planner knob removed, conflict groups
  stated.
- `CONTEXT.md` — **Shard** gains the conflict-group and responsibility-set notes;
  **Baseline** gains the composed-figures note.

## What is being built

### 1. The responsibility set — do this first, it is the load-bearing change

A run reports exactly the components it is responsible for: its `--component`
list, or the whole declaration when there is none. Nothing about any other.

- `coverage.go`'s `inDeclarationOrder` currently pads a `coverage(<name>)` row for
  every *declared* component. Scope it to the responsibility set. This is the
  defect that makes a shard publish rows about other shards' components.
- `test.go`'s affected `skipped` map is intersected with the responsibility set,
  so `test(a): not affected` is emitted by the one shard that owns `a`.
- `coverage.go`'s `recordingBlockedBy` is asked only about components the run
  *selected* and failed to measure. A component it never ran is simply absent.
- Drop the `--affected` + `--component` refusal in `newTestCmd` and rewrite the
  comment: `--component` says what this job is responsible for, `--affected` says
  which of those need running. AGENTS.md's justification for the refusal goes with
  it.
- A run narrowed by `--component` emits **no** `coverage(repo)` / `patch(repo)`
  rows. An unnarrowed run still does.

**Wire it to a caller before moving on.** `lydite test --component cli` from the
repository root, and `--affected --component cli`, and check the row set by eye.

### 2. `measurements.json`

Rename `baseline.json` → `measurements.json`, `candidateDoc` → the measurements
document, and extend each component's entry with:

- its patch part (`Hit`, `Total` changed lines) — `patchPart` in `coverage.go`;
- the baseline entry it was gated against (`LineCount` + producer), so `merge`
  composes without touching the network.

Keep the existing fields (`covered`, `total`, `producer`, `carried`) and the
tree binding. `record` ignores the new fields; `merge` needs them.

`readCandidate`'s refusal of a document naming no tree stays exactly as it is.

### 3. One composition, two callers

Pull `composedRow` and `composedPatchRow` behind one entry point taking
`[]measurement` + `[]patchPart`, called by `lydite test` (unnarrowed) and by
`merge`. Two implementations that agree today is the thing this repo refuses
everywhere — see `internal/pathmatch`, `internal/cargotool`, `internal/download`.

### 4. `lydite test plan`

Pure: reads `.lydite/components.yml` and each component's compose file, and
nothing else. No git, no network, no process.

- A shard is the transitive closure of `scheduler.Conflicts` over the declared
  components. `scheduler.Conflicts` already returns the pairs — **share it, do
  not reimplement the predicate.** Note it returns one entry per *thing* shared,
  so a pair sharing two ports is one edge; `scheduler.Pairs` shows the existing
  handling of that.
- Shards ordered by the declaration position of their first member; members in
  declaration order. Two runs of one declaration emit an identical matrix.
- Report to stdout through `internal/ui`, one row per shard, naming why a group
  is a group. Matrix to `--out <file>`:
  `[{"name":"api-tally","components":"api,tally"}]`.
- `name` is the members joined by `-`; unique because component names are.
- **No `.lydite-reports/plan.json`.** ADR 0026 states why; do not add one to be
  consistent with the other commands.
- No `--shards` knob. No `--concurrency`.

### 5. `lydite test merge`

`--dir` (for the declaration) and repeatable `--reports`. No network.

- Concatenates per-component rows in declaration order.
- A declared component with no row from any input → **fail**, through a `shards`
  row. Two rows → **fail**. Not `unmeasured` — it does not vote, and a dead
  runner would publish `"verdict": "pass"`.
- `orphans` / `watch` / `select` rows are identical across shards, so they
  collapse to one; if they *disagree*, fail — the shards saw different trees.
- The per-shard `schedule` rows fold into one: `N shard(s), max K concurrent`,
  with a detail line per shard naming its serialised pairs. `.github/assert-proving-ground.py`
  reads this today; check it against a report from a real run.
- Computes `coverage(repo)` and `patch(repo)` once, from the measurements.
- Writes `.lydite-reports/test.json`, so `publish` renders one `test` section.
- Verdict is the worst of the inputs; `ui.Report.ExitCode` stays the only place
  the verdict→exit mapping lives.
- Tolerate a shard run with `--no-coverage`, which writes no measurements at all.

`record` is unchanged: it keeps folding the same `--reports` directories itself,
because it runs post-merge in a workflow `merge` does not precede.

### 6. The workflows

- `lydite-pr.yml`: `setup` → `plan` → `test` (matrix, `--affected --component
  <slice> --gate-coverage`, `contents: read`) → `merge` → `publish`.
- `lydite-baseline.yml`: `plan` → `test` (matrix, `--gate-coverage`, no
  `--affected`) → one `record --reports <each shard's dir>`, `contents: write`.
- Artifact per shard, named from the matrix `name`. `.github/actions/lydite-reports`
  takes a name already.
- **Never interpolate `${{ }}` into a `run:` body** — `env:` block, then `"$VAR"`.
  Semgrep's `yaml.github-actions.security.run-shell-injection` has caught this in
  this repo before.

### 7. `ci-end2end.yml` — a second job, `proving ground — coverage gate`

This is #48, and it is the assertion that the whole gate works against a real
repository rather than a fixture. Its own runner, its own local bare origin.

1. `plan` → 3 shards: `{api, tally}` together (they share 5432 under services
   deliberately named `db` and `postgres`), `sdk` and `web` alone. No suites.
2. Push base commit `B` to the local bare origin as `main`. Probe branch `P`
   lowering one component's coverage and adding an untested line.
3. `test --affected --gate-coverage` on `P` → assert **baseline miss**, base tree
   measured in a worktree, the fallen component **fails**, untouched ones do not,
   a patch row for the changed component, unrun components **carried forward**
   with the composed rows naming the counts.
4. Check out `B`; `test --gate-coverage` + `test record` → assert
   `v4/<treeB>.json` holds one entry per declared component and none for
   anything undeclared.
5. Back on `P`, run the three shards **sequentially in this one job** via
   `--component`, each with its own reports directory → assert **cache hit**: the
   base tree is measured once, not twice.
6. `merge` over the three directories → assert one row per declared component,
   `coverage(repo)` and `patch(repo)` present, gate rows collapsed.
7. `merge` again with one directory **omitted** → assert it **fails** naming the
   absent component. This is how a dead runner is exercised without one dying.

**Move the existing `lydite test record (proving ground)` step out of the first
job and into this one** — steps 4–5 are a strict superset, and paying for it
twice is a full extra pass. It must move in the same change, not after it: that
step is the guard that stopped the silent-recorder failure recurring.

Assert the negative throughout, as the existing modes do. "The run was green" is
satisfied by a gate that compared nothing.

### 8. Documentation

- AGENTS.md: the planner, `merge`, the responsibility set, the sharded workflows,
  and the rewritten `--affected` + `--component` paragraph.
- `.agents/plans/component-platform.md`: step 8 done.
- `.agents/plans/pr9-mutation.md`: the next session's prompt, same shape as
  `pr8-plan-and-merge.md` — mutation for all three languages, on the runner's
  three variants and the coverage measurement that already exists.

## File an issue before opening the PR

**`lydite/actions`: shard the reusable workflow** — `plan` → matrix → `merge`,
and the artifact-per-shard convention. #61's third bullet is "the reusable
workflow", which lives in a repository that is not here, so #61 cannot close
without the remainder being filed first. It is a sibling of
[#87](https://github.com/lydite/lydite/issues/87) (the `record` step), not a
duplicate — file it separately and cross-reference.

Out of scope and staying open: mutation (step 9), #89 and #55 (the producer and
toolchain-probe residuals), #75 and #34 (both need a `pedromvgomes/gt` change).

## What this repository will do to you

- **Pushing over SSH fails in this sandbox** (`Permission denied (publickey)`),
  silently when piped. `origin` is `git@github-personal:lydite/lydite.git`; push
  and fetch over HTTPS with a `gh` token:
  `git -c "url.https://x-access-token:$(gh auth token --user pedromvgomes)@github.com/.insteadOf=git@github-personal:" fetch origin main`
- The same block means `--gate-coverage`, `--affected`, `scan --diff-base auto`
  and `review --base auto` cannot resolve a merge-base locally. To exercise them,
  clone into the scratch directory with a `file://` origin and run there. That is
  how the record/gate flow was verified end to end last session.
- **The proving ground needs a container runtime** for two of its four
  components, and there is none here. Everything asserting against it is
  validated by CI on the PR, not locally. Budget for one red run.
- `.github/assert-proving-ground.py` reads row labels and has three modes today
  (`--affected`, `--scan`, `--record`). Any label change means checking it
  against a report from a real run.
- `status` is read-only in zsh; workflow `run:` bodies are bash, where `status=0`
  is fine and is what the existing steps use. Do not "fix" the workflows to match
  a local shell.
- Running `lydite test record` locally pushes to `origin/lydite`. Use a scratch
  clone unless you mean it.
- A force-push can start two `ci-orchestration` runs a second apart; the one the
  concurrency group cancels reports as a failed `ci-gate`. Re-run it.
- A Biome suppression must be the line immediately above what it suppresses.
- `cloud-services` declares no `@cloudflare/workers-types`, and must not.
- Chained shell commands fall through, and `cd` can silently fail. Absolute
  paths; verify an edit landed rather than that a command exited 0.
- Every PR here is referred and needs `/lydite clear`. A push voids it.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...`
  URL — not even when a harness asks for one. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. File the `lydite/actions` issue first.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no "before X existed", no
  ticket numbers, no roadmap. ADRs are the exception. This is the repo's
  most-violated rule, and a reviewer caught three violations of it last session
  in the change that added the rule's newest example.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync` /
  `gt repo check`. `ci-end2end.yml`, `lydite-pr.yml` and `lydite-baseline.yml`
  are not generated — their headers say so — but check before editing any other.
- Ask before editing CI beyond the three workflows named above.
- Before proposing: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `source/cli`, plus `lydite scan --dir .` and
  `gt repo check` from the root.
- Run `/code-review` before proposing the PR and again after acting on it. Keep
  going while rounds return findings that change behaviour; stop when what comes
  back is wording and test robustness, and say that the trend is the reason
  rather than claiming the work is clean.

## Working mode

- **Wire new code to a caller early.** `plan` and `merge` with no workflow calling
  them are a package nothing imports, which is exactly how step 7's defect
  survived a clean build, a clean lint and a passing suite.
- Run it with something missing. `merge` with a directory absent, a shard with
  `--no-coverage`, a declaration with one component.
- Prefer one shared implementation over two that agree today.
- Verify a claim against the repository rather than against this file.
