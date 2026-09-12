# Session prompt — PR 3 of 3: the finding count reaches the quality history

Continues `pr20-scanners-emit-findings.md`, which merged as
[#129](https://github.com/lydite/lydite/pull/129) (`f395478` on `main`) and closed
[#128](https://github.com/lydite/lydite/issues/128) — every scanner now reports its findings as
data, and `scan` anchors them against the change. **This is the last of the three.**

## Setup — do this before anything else

You are in a `gt`-powered bare repo at `/Users/pedrogomes/work/repositories-personal/lydite/`.
**Work in the worktree you are already in — do not create another.** All three PRs used this one.

**Point the worktree at a fresh branch off the current `main`:**

```sh
export GH_TOKEN="$(gh auth token --user pedromvgomes)"
git -c credential.helper='!gh auth git-credential' \
    fetch https://github.com/lydite/lydite.git main:refs/remotes/origin/main --force
git log --oneline -1 origin/main          # must carry #129's squash (f395478), or later
git checkout -b feat/findings-reach-the-ledger origin/main
```

Check the fetch actually succeeded. **SSH is blocked** — `origin` is an SSH alias, so a plain
`git fetch` fails with `Permission denied (publickey)` and leaves `FETCH_HEAD` stale.

The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes`. Use it for every `gh` command.

**This PR closes [#111](https://github.com/lydite/lydite/issues/111)** — the issue is already the
right size for this slice, so there is nothing to carve. End the body with `Closes #111`.

## The slice

`lydite scan` writes every scanner's findings into `scan.json` under a top-level `findings` key.
Nothing counts them. [ADR 0009](../../docs/adr/0009-quality-history-storage-and-access.md) names
a finding count as the fourth scalar the quality history holds, and the ledger records the other
three.

### Decisions already taken. Do not re-litigate them.

1. **`ledger.Component` gains a per-gate map, scanner gates only.** `0` means the gate ran and
   found nothing; **absent means it does not apply to that language** — ADR 0029's "absent is not
   zero", which a single integer cannot express. CRAP and patch stay out: `CRAP.Above` already
   *is* the count of CRAP findings, and recording it twice makes "two quantities that must
   agree" into "two quantities free to disagree".
2. **The channel is `scan.json`**, which `saveDocument` already writes unconditionally, read out
   of each `--reports` directory by `lydite test record`. **`measurements.json` keeps its single
   writer** — `findings.md` already argues this for the neighbouring case, and `measurementsDoc`
   refuses to load without a `Tree` a scan has no business asserting.
3. **A scan job in `lydite-baseline.yml`**, mirroring `lydite-pr.yml`'s but **without
   `--diff-base`**: on `main` there is no change to scope to, so it covers the whole repository,
   every finding lands `AnchorNowhere`, and the count is the repository's standing total.
   `record` gains `scan` in its `needs` and keeps `if: ${{ !cancelled() }}` — **a red scan must
   still record**, because a red scan on `main` is the most interesting thing a finding history
   can hold, and no later run can fill the hole since the next push is a different tree.
4. **Artifact naming**: `record` downloads `pattern: lydite-shard-*`. Upload the scan's reports
   under a **distinct** prefix — it is not a shard — and add a second `download-artifact` step
   into the same `shards/` directory so the existing `for dir in shards/*/` loop picks it up.
5. **Refine `measurementsIn`'s row** for a directory holding a scan document but no
   `measurements.json`. It is tolerated today but renders as an amber "no measurements", which
   reads like a malfunction where it is the expected shape.

### What #129 changed that this PR must know

- **`scan` anchors its findings.** It asks `coverage.ChangedLines` once and anchors in `record`,
  after `labelled` has rebased each path onto the scan root. A scan with no `--diff-base` anchors
  nothing, which is exactly decision 3's shape and is now a tested property
  (`TestAScanOverAWholeRepositoryAnchorsNothing`).
- **The diff base is resolved whenever `--diff-base` is given**, including under a
  `SEMGREP_APP_TOKEN` and with Semgrep switched off — the anchor is a second reader, and the
  token rule moved to `semgrepBase` at Semgrep's own call site. **A token-bearing consumer
  passing `--diff-base auto` now needs `fetch-depth: 0`.** The baseline job passes no
  `--diff-base` at all, so it needs no such fetch; say so in the workflow rather than leaving a
  reader to infer it.
- **The base is resolved to a SHA** by `rev-parse --verify --end-of-options` before anything is
  given it, so what reaches git and Semgrep is never the caller's string.
- Six parsers, their argv builders and the `*Result` halves are in `internal/{golang,rust,
  semgrep}`. A finding count reads `ui.Document.Findings` and needs none of them.

### Tests

- A component with a scanner gate that ran and found nothing records `0`, and one whose language
  has no such gate records **absent** — the distinction is the whole of decision 1, so it needs a
  test that fails if the map collapses to zero.
- `record` folds a scan document out of a report directory that holds no `measurements.json`.
- A red scan still records.
- The row for a directory holding a scan document and no measurements is not amber.

**Prove each new test fails without its fix.** A `go test -run` that matches nothing prints `ok`
— confirm the test actually ran with `-run '<Name>' -v` and read the `=== RUN` lines.

## Gates — before you commit, and again before you propose

From `source/cli`, with `GOTOOLCHAIN=local` and **the release build tags**:

```sh
GOTOOLCHAIN=local go build -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
GOTOOLCHAIN=local go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
golangci-lint run ./...
```

From the repository root: `lydite scan --dir .` and `gt repo check`.

**`golangci-lint` skips analysis using cached facts and then reports `0 issues` having run
nothing.** A planted defect is the only way to know the check is live — and it must be one that
*compiles*: an unused function trips `unused`, while a `declared and not used` variable is a
compile error the linter reports as `typecheck`, which proves nothing about the analysis.

**Run the coverage gate locally before pushing.** `#129` was pushed green on `go test` and failed
CI on three gates at once:

```sh
cd source/cli && GOTOOLCHAIN=local go run ./cmd/lydite test --dir ../.. --component cli --gate-coverage
```

Its `baseline` row needs `git fetch origin`, which fails here because `origin` is an SSH alias —
the `coverage`, `patch` and `crap` rows above it are still the ones worth reading.

## Loop A — review the commit, before the PR exists

**At most 5 turns.** Each turn:

1. Run `/panel-code-review` on the last commit. Pass both ends:
   `--base <sha>~1 --head <sha>` — `--base` alone reviews the commit *plus* the working tree.
2. **Fix every valid finding, including GREEN ones.** A finding you judge invalid is not silently
   dropped — say which one and why, in your reply to the user.
3. Re-run the full gate list above.
4. Amend the commit, so "the last commit" and "the branch" stay the same object.

**Stop when a turn returns zero findings, or at 5 turns — whichever is first.**

**Budget, measured across #129's eight turns: $20–26 per turn, not the ~$4 an earlier plan
estimated.** The `deep` panel's security reviewers run on Opus 5. Report the cost and continue.

**The `deep` panel runs six concurrent Claude reviewers and the OS will kill it when the machine
is loaded.** A killed run leaves a **0-byte result file**, and an empty finding list from a failed
run is indistinguishable from a clean pass — it is a failure that reached no verdict, so do not
count it. Check free memory before blaming the panel: long-running `claude` sessions accumulate
(ten of them held 3.2 GB during #129), and `agtk` itself leaves nothing behind — verified, no
orphaned reviewer process survives a run. If it is killed twice, say so and either ask the user
to close some sessions or drop to `--panel standard` and state plainly what that gives up: two
reviewers instead of six, no performance axis, and no corroboration signal.

## Open the pull request

```sh
git -c credential.helper='!gh auth git-credential' \
    push https://github.com/lydite/lydite.git feat/findings-reach-the-ledger:feat/findings-reach-the-ledger
```

PR title is the commit subject and must conform to Conventional Commits, because `gt`
squash-merges and the title becomes a commit subject on `main`.

**Force-pushing needs the expected value spelled out.** Pushing by URL updates no
remote-tracking ref, so a bare `--force-with-lease` fails with `stale info`. Read the remote head,
check it is what you last pushed, then name it:

```sh
git -c credential.helper='!gh auth git-credential' ls-remote https://github.com/lydite/lydite.git refs/heads/<branch>
git -c credential.helper='!gh auth git-credential' \
    push --force-with-lease=refs/heads/<branch>:<expected-sha> https://github.com/lydite/lydite.git <branch>:<branch>
```

**Keep the PR body current as the branch changes.** #129's body described only its first review
loop and never mentioned the work that followed; the author had to be told.

## Loop B — review the pull request

**At most 5 turns**, same shape as Loop A, plus: reply to every open comment on the PR, including
ones you are not acting on.

**A PR target selects `deep-codex`, and codex has been over its usage limit through both previous
PRs.** When every reviewer answers "You've hit your usage limit", the run posts an *empty* review
that reached no verdict — a failure, not a clean pass. Re-run with `--panel deep --force` and say
you overrode it and why, or ask the user whether to skip Loop B as they chose for #129.

### About CI on this PR

- **The `referral` job exits 0 on a referral.** `lydite/referral` stays *pending* until a human
  comments `/lydite clear`, and `.lydite/exemptions.yml` does not exist — so **every** PR here is
  referred. A green job says nothing; read the commit status.
- **Removing or renaming a test trips the `tests removed` referral check.** Say in a PR comment
  where it went.
- **Poll by commit SHA**: `gh run list --commit "$(git rev-parse HEAD)"`. `--limit 1` right after
  a push reads the *previous* run.
- **`gh pr checks` exits non-zero when any check is not green**, so `s=$(gh pr checks …) || …`
  swallows every poll.
- **Pushing cancels the in-flight run** — `lydite-pr.yml` sets `cancel-in-progress`.
- **The Bash tool caps a foreground command at ten minutes.** Check CI once, report, move on.
- **`gt` merges by squashing and pushing**, so `gh pr view <n> --json merged` can read `false`
  on a merged PR. **Read `main`'s head.** (It does report `MERGED` once `gt` has pushed — but the
  head is the fact.)

## When both loops are done

**Do not merge.** Never merge a PR unless explicitly told to.

This is the last of the three, so there is no continuation prompt to write. Tell the user the
sequence is complete and what remains below.

## Owed, and not this PR's work

- **Seven plan files are untracked** in `.agents/plans/`, including this one. Plans land in their
  own `docs(plan):` pull request, never with a feature — and that PR needs its own issue.
- **Four memory candidates are staged** in `.agents/memory/candidates/`:
  `pullrequestfromref-head-now-documented`, `rust-fmt-still-fails-scan`,
  `issue-123-still-open-unfixed` (made false by #127) and
  `annotation-grammar-excludes-scanner-findings-by-design`. **Run `/memory-curate`.** You do not
  hand-edit the store or `INDEX.md`.
- **[#130](https://github.com/lydite/lydite/issues/130)** — `[lydite:exclude_from_coverage]` is
  inert in Rust and TypeScript: `ParseLCOV` honours no exclusions, so a declaration there excludes
  nothing, is not reported as unused, and still trips referral. Opened during #129.
- **`lydite/actions` still carries the `contains` bug** in its packaged `lydite-comment`: the
  marker matches anywhere in a body, so a person quoting lydite's verdict is the comment the next
  run `PATCH`es wholesale. One-word `jq` fix (`contains` → `startswith`, with a `// ""` guard for
  a null body). That repo is not checked out here. **It needs an issue.**
- **`lintDirBiome`'s coverage annotation claims tests that do not exist** — "internal/typescript's
  own tests assert as argv", and `biome_test.go` asserts no argv at all. #129 made the claim true
  for the six scanners it added (`gosecArgv`, `govulncheckArgv`, `clippyArgv`, `auditArgv`,
  `denyArgv`, `reportArgs`, each with a test). Biome's is still owed.
- **The two-run shape for `cargo-audit` and `cargo-deny` is unratified.** #129's decision 1 says
  every tool keeps printing what it printed and `Result.Detail` stays empty, and their JSON flags
  replace the human output rather than copying it — so they run twice. Measured on a 377-package
  lockfile: **+1.9s each, near-flat in graph size** (the cost is loading the RustSec DB, not the
  lockfile). The user asked about this twice and never chose; the alternative is one JSON run with
  lydite rendering `Detail`, biome-style.

  **clippy's second pass costs one warm clippy invocation, and does not scale with the build.**
  Measured against `wardnet`'s daemon — 10 workspace members, 666 lockfile packages, 718 files,
  212K lines — six alternating rounds on one warm cache gave text `8.91 17.66 17.93 17.09 19.42
  16.13` and JSON `8.57 41.03 18.58 15.33 15.70 18.79`. **The two are the same within noise**:
  the JSON pass is not systematically slower, because neither recompiles — both freshness-check
  the unit graph and replay cached diagnostics. So the second pass adds about **9s quiet, 15–19s
  on a loaded machine**, against a cold first pass of ~140s. It scales with the number of units,
  not lines of code: 1 crate cost 0.24s, 666 packages cost ~9s.

  Two caveats on that figure, both making it a **lower bound**. `wardnet`'s own crates never
  compiled: `rustables`' build script runs bindgen against `linux/netlink.h`, so the daemon
  cannot be clippy-checked on macOS **at any target** — `--target aarch64-unknown-linux-gnu`
  fails the same way, because a build script reads *system* headers. Their analysis would be
  replayed by both passes equally, so the delta stays one warm invocation, but the absolute
  numbers would rise. And the host carried a load average of 27 throughout.

  **Read the JSON stream, not the clock.** The first attempt at this measurement reported a
  plausible 160.7s/10.1s before `build-finished: {"success": false}` showed the build had died
  on Darwin's missing netlink symbols — a wall-clock number looks entirely reasonable when the
  work never happened. `/usr/bin/time -p cargo … | grep real` also reports *grep's* exit status,
  which hid cargo's 101 twice.
- **`docs/release-notes/v0.2.0.md` is on `main` and stale** — no mutation, names
  `.lydite-reports/baseline.json` which is `measurements.json`, no `test plan`/`merge`/`record`,
  and now also missing the CRAP gate, the `[lydite:exclude_from_<gate>]` grammar, ADR 0028, the
  ledger with ADR 0029, the findings channel with ADR 0030, the review threads with ADR 0031, the
  scanner findings with ADR 0032, and the `AGENTS.md` split. **Its own pull request**, and it must
  be correct before any tag — `release.yml` reads it out of the tagged tree.
- **[#109](https://github.com/lydite/lydite/issues/109)** — a mutant is bounded in time and not in
  memory. **It fired again during #129**: a mutant dropping `i--` from a manual reverse index in
  `govulncheckTrace` turned an appending loop unbounded, the job exited 143 reporting
  "interrupted", and the run never finished enumerating its mutants. Prefer `range` and
  `slices.Backward` over a manual index — a range has no counter to drop.
- **[#112](https://github.com/lydite/lydite/issues/112)** — mutation scalars in the ledger,
  structurally blocked behind #49.
- **[#103](https://github.com/lydite/lydite/issues/103)** — `lydite mutation` has no job in
  `lydite/actions`.

## The gate on the release

**Nothing ships until everything but the dashboard ([#27](https://github.com/lydite/lydite/issues/27))
is done, and that explicitly includes the relay being deployed** so `vars.LYDITE_RELAY_URL` is
set. The relay App (`LYDITE_APP_ID=4814607`) is registered but **not installed** on the org.
`lydite/actions` `@v1` still points at `e936433`. **Do not move `@v1`, and do not tag a release.**
The order is fixed: a lydite release first, because `setup@v1` resolves `latest`; only then does
`@v1` move.

## Environment

- **SSH is blocked.** Push and fetch over HTTPS with the gh credential helper, as in Setup.
- **When a lydite command shells out to git itself** — `mutation`, `test --gate-coverage`,
  `review`, `scan --diff-base auto` all run `git fetch origin <branch>` in a child process —
  `git -c` cannot reach it. Pass the rewrite through the environment:

  ```sh
  export GH_TOKEN="$(gh auth token --user pedromvgomes)" \
    GIT_CONFIG_COUNT=2 \
    GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf GIT_CONFIG_VALUE_0=git@github-personal: \
    GIT_CONFIG_KEY_1=credential.helper GIT_CONFIG_VALUE_1='!gh auth git-credential'
  ```

- **No container runtime**, so nothing touching the proving ground runs locally.
- `lydite scan` runs from the repository root (`--dir ../..` from `source/cli`). `--dir ..` names
  `source/`, which declares no components and errors.
- A full `go test -race ./...` is roughly four minutes. `lydite test --component cli
  --gate-coverage` is about five; `lydite mutation --component cli` was seventy-eight minutes on
  #124's diff and twelve on CI for #129's. Run the long ones in the background.
- **`npx vitest run --coverage` leaves `source/cloud-services/coverage/`** in the tree, and the
  next `lydite scan` fails on Semgrep findings inside the generated `prettify.js`. Delete it
  before scanning.

## Traps previous sessions sprung

- **A `go test -run` that matches nothing prints `ok`.** Always confirm a new test actually ran.
- **`golangci-lint` skips analysis using cached facts**, then reports `0 issues` having run
  nothing. Plant a defect that *compiles* — an unused function, not an unused variable.
- **A test asserting `Contains` can survive the mutant it was written for.** #129's `buildErrors`
  test checked for `"main.go"` and stayed true when a mutant swapped the located and aggregate
  entries, because the aggregate text names the file too. `HasPrefix` was what bit.
- **Restructuring past a boundary mutant can trade it for its mirror.** #129 changed `i >= 0` to
  `i > 0` because the mutant was the more correct code — and the next run flagged `i > 0` →
  `i >= 0`. The test for the boundary case is what actually removes it.
- **A mutant can be genuinely equivalent.** Prefer restructuring until no test could tell the
  difference — `tomlString` dropped a length check for `CutPrefix`/`CutSuffix`, `decodeNDJSON`
  dropped an emptiness check a failed `Unmarshal` already covered. Only then annotate, with a
  trailing `// [lydite:exclude_from_mutation][<reason>]` **on the statement**: the declaration
  attaches to the innermost mutant span containing its line, so a doc comment above the function
  matches nothing.
- **A coverage exclusion also clears CRAP** (`byScore || byCoverage` in `internal/crap`), so one
  `[lydite:exclude_from_coverage]` answers both gates.
- **`coverage.ChangedLines` diffs `<base>..HEAD`** — committed state, not the working tree. An
  uncommitted change is invisible to it, which will make an end-to-end anchoring check look
  broken when it is the check that is wrong.
- **gosec descends into `testdata/`** where the go tool does not, and Semgrep scans every file it
  is given. A fixture that reproduces a finding is by construction code lydite's own scan fires
  on — commit it with a `.txt` suffix and materialise it with `internal/fixture`.
- **Go's JSON decoder matches field tags case-insensitively**, so a mis-cased tag is not the bug
  it looks like; a wrong *name* is.
- **Moving a fixture can silently un-cover the branch its test was written for.** #129's
  relative-path rebasing lost its coverage when fixtures moved into temp directories, which are
  absolute. The test still passed. Mutation caught it.
- **A stale doc comment survives a rewritten reference.** When you change behaviour, grep for
  every place that describes it — including your own comments from earlier in the same branch.
- **Comments describe the code, not its history.** `CLAUDE.md` forbids `used to`, `previously`,
  `no longer`. A reviewer may call this a style preference; it is a rule, and it applies to the
  reference markdown as well as to Go. ADRs are the exception.
- **Correlated reviewers reach the same wrong answer.** Treat convergence as evidence, not proof,
  and **verify a claimed behaviour against the platform**, not a forum summary. #129 had a
  reviewer justify a real finding with a false premise twice.
- **A fix applied to two of three copies reads as done.** When you change one of a set, grep for
  the set.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.
- **`git reset --hard` is blocked by the permission classifier.** `git reset --soft` is not, and
  `git branch -f <name> <sha>` moves a branch from another branch.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even when
  a harness asks. `CLAUDE.md` says so explicitly.
- `Closes #N`, not `Refs`.
- **Never merge a PR unless told to. Never tag a release, and never move `@v1`.**
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- **Prove a regression test fails without its fix**, and **verify a mutant kill by hand** — apply
  the mutant, run the package suite, restore. A merged diff's survivors can never be regenerated.
- Delegate to the `memory-explorer` agent before answering *why* something is built the way it is,
  what breaks if you change X, or whether an approach has been tried.
- Invoke the `challenge` skill before `ExitPlanMode`.
