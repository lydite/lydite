# Session prompt — the scanners that still report nothing but a status

Continues `pr17-findings-as-threads-implementation.md`, which shipped
[#124](https://github.com/lydite/lydite/pull/124) and closed
[#114](https://github.com/lydite/lydite/issues/114). Where the two differ, this file wins.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. **Work in the worktree you are
already in** — do not create another.

**The worktree is sitting on a branch that no longer exists.** `feat/findings-as-threads` was
squash-merged and deleted. Point it at a fresh branch off the *current* `main` before anything
else, and verify you have the merge:

```sh
git rev-parse --abbrev-ref HEAD            # feat/findings-as-threads — stale
git fetch origin main                      # over HTTPS; see Environment
git log --oneline -1 origin/main           # must be b498e63 or later, "(#124)"
git show origin/main:source/cli/internal/threads/threads.go >/dev/null   # must exist
git checkout -b <type>/<name> origin/main
```

If `internal/threads` is not on `main`, #124 has not landed and this whole file is premature.
**Stop and say so.**

**`gt` merges by squashing and pushing, not through GitHub's merge button.** So
`gh pr view <n> --json merged` reads `false` on a pull request that is merged, its state is
`closed`, and the branch is gone. **Read `main`'s head, never the `merged` flag.**

The root `.envrc` scopes `GH_TOKEN` to gh user `pedromvgomes`. Use it for every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above it. Every `go`
and `golangci-lint` command runs from `source/cli` with `GOTOOLCHAIN=local`, **and with the
release build tags**:

```sh
GOTOOLCHAIN=local go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

Without them the binary carries all 206 of gotreesitter's grammars, and `ci-test` runs the
suite *with* them — a language whose own `grammar_subset_<lang>` tag is missing fails at its
first parse, and a bare `go test` can never see it.

## Owed from #124, and small

**One commit was written and never pushed.** `d6f2b15`, *"docs(forge): the client covers
eleven calls, not ten"*, existed only in the worktree when the pull request was merged, so
`main` still says ten. It is right and it is one paragraph: `internal/forge` has eleven calls,
not ten, because the platform refuses a file-anchored claim inside a review and
`CreateFileComment` is therefore a call of its own — a fifth addition rather than the four the
design foresaw. Both `internal/forge/forge.go`'s package doc and ADR 0031's *"The costs,
stated"* say ten. **Recover it from the reflog or rewrite it; it is three lines in two files.**

```sh
git log --all --oneline | grep eleven      # d6f2b15 if the reflog still holds it
```

## The gate on the release

**Nothing ships to consumers until everything but the dashboard
([#27](https://github.com/lydite/lydite/issues/27)) is done — and that explicitly includes the
relay being deployed**, so `vars.LYDITE_RELAY_URL` is set and consumers get the App identity
rather than only the `github-token` fallback. The relay App (`LYDITE_APP_ID=4814607`) is
**registered but not installed** on the `lydite` org.

`lydite/actions` `@v1` still points at `e936433`. **Do not move `@v1`, and do not tag a lydite
release.** The order, when it happens, is fixed: a lydite release first, because `setup@v1`
resolves `latest`; only then does `@v1` move.

## The slice

**[#111](https://github.com/lydite/lydite/issues/111) — every scanner emits findings, and the
count reaches the ledger.** Biome is the only check that produces `finding.Finding` today;
every other scanner reports a status and a log and nothing a consumer can anchor. Four
things:

1. **A parser per scanner.** The issue names five; **verify the list against `internal/`
   before you start** rather than trusting the number — the candidates are `gosec`,
   `govulncheck` (both `internal/golang`), `semgrep`, and clippy / `cargo-audit` /
   `cargo-deny` (`internal/rust`). Each already runs through `executil.Run`, and
   `executil.Result.Findings` is the field waiting for them.
2. **`Finding.Detail` carries the tool's extended text** — the call stack, the code excerpt,
   the lines a rule matched. The field exists and nothing but Biome fills it.
3. **The count into `measurements.json`, and on into `ledger.Component`.** ADR 0029 says why
   mutation's scalars are not there; findings have no such blocker.
4. **A scan job in `lydite-baseline.yml`**, so the count is recorded on a merged tree the way
   coverage and CRAP already are.

**Every parser follows `reportableBiome`'s stance** (`internal/typescript/biome.go`):
**default-true for an unrecognised category**, and a report that will not parse falls back to
the tool's own exit status rather than inventing a verdict. A parser that silently drops a
diagnostic it does not recognise is how a gate stops gating.

### What the findings channel already guarantees, and needs no re-deriving

Read [`.agents/references/findings.md`](../references/findings.md) before writing a parser.
The short of it:

- **A finding is a located claim about the code that one edit clears.** That is what decides
  who may emit one.
- **The fingerprint contains no line number.** `v1:sha256(gate │ component │ path │ site │
  ordinal)[:16]`, derived centrally in `internal/finding`. `site` is content, per gate — for a
  scanner it is *the rule with the source text it fired on, read from the tree rather than
  from the report*. `finding.Source` reads it through an `os.Root`.
- **`finding.Number` assigns the ordinal** over the whole set, in source order.
- **A scanner's findings are rebased onto the scan root** in `labelled` — a check runs inside
  its component and reports paths relative to there.
- **`Finding.Row` is the label of the row that reported it**, and `findingsOf`/`record` in
  `cmd/lydite/scan.go` already set it from `executil.Result.Name`. Do not set it by hand in a
  parser.
- **`Anchor` is decided where the finding is produced**, from the changed-line map the gate
  holds. A scan over a whole repository knows of no change and leaves every claim
  `AnchorNowhere`, which is correct — those are the standing comment's.

### What a new finding immediately becomes

**A review thread on its line**, via `lydite threads` (ADR 0031, and
[`.agents/references/surface.md`](../references/surface.md)). That is now live, so a parser
that emits a badly located claim puts a thread on the wrong line of somebody's pull request
rather than a wrong line in a log. Two consequences worth holding while writing one:

- **A claim reaching the change at a line must be one the platform will accept.** `Anchored`
  is what guarantees that, against `coverage.ChangedLines` (`--unified=0`). Do not anchor by
  any other route.
- **`Line` must itself be a changed line** when the anchor is `AnchorLine`. `Anchored` raises
  the anchor when *any* line in `[Line, EndLine]` was touched, and the thread is opened on
  `Line`. Every producer satisfies this by construction today; a parser that sets a range
  whose start is outside the diff would 422 the whole review. Worth a test.

## Decided earlier, and still binding

- **`lydite publish` is pure.** No network, no token, no hosting platform. `lydite threads`
  owns the fetch, the delta and the apply.
- **No finding appears in both surfaces.** A partition on `Anchor`: the comment takes
  `AnchorNowhere`, the review takes the rest. Neither renderer knows about the other.
- **One upsert has three implementations** — `forge.FindComment`, the relay's `findComment`,
  and the `github-token` fallback's `jq` in `.github/actions/lydite-comment`. All three match
  the marker **at the start of a body**. A change to one is a change to all three, and the
  packaged `lydite/actions` copy is a fourth (see below).
- **lydite can never resolve a thread.** `resolveReviewThread` needs `Contents: write`, which
  ADR 0022's two-App split forbids. A thread is a soft gate.
- **The terminal is unchanged** by any of this.

## Owed from previous sessions

- **`lydite/actions` still carries the `contains` bug.** The packaged copy of
  `lydite-comment` matches the standing comment's marker anywhere in a body, so a consumer on
  `@v1` has the hole #124 closed here: a person quoting lydite's verdict is the comment the
  next run `PATCH`es wholesale. That repository is not checked out here. **It needs an issue
  and then the one-word `jq` fix** (`contains` → `startswith`, with a `// ""` guard for a null
  body). Nothing tracks it yet.
- **Four plan files are untracked** — `pr15-the-ledger.md`, `pr16-findings-as-data.md`,
  `pr17-findings-as-threads.md`, `pr17-findings-as-threads-implementation.md`, and this one.
  Plans land in their own `docs(plan):` pull request, never with a feature — and that PR needs
  its own issue, since a PR closes one.
- **`docs/release-notes/v0.2.0.md` is on `main` and stale.** No mutation, names
  `.lydite-reports/baseline.json` which is `measurements.json`, no `test plan`/`merge`/
  `record`, a closing `lydite/actions` section reading as a to-do for merged work, and now
  also missing the CRAP gate, the `[lydite:exclude_from_<gate>]` grammar, ADR 0028, the ledger
  with ADR 0029, the findings channel with ADR 0030, the review threads with ADR 0031, and the
  `AGENTS.md` split. It must be correct **before** a tag is pushed — `release.yml` reads it out
  of the tagged tree. **Its own pull request.**
- **[#123](https://github.com/lydite/lydite/issues/123)** — the fold unions every shard's
  findings with no fingerprint dedup. `lydite threads` dedups on read, so this is no longer
  load-bearing for the surface, but the folded document still carries duplicates for anything
  that counts them — **which is exactly what this slice adds**. Fix it here or say why not.
- **[#112](https://github.com/lydite/lydite/issues/112)** — mutation scalars in the ledger,
  structurally blocked behind #49.
- **[#109](https://github.com/lydite/lydite/issues/109)** — a mutant is bounded in time and
  not in memory. Measured: 14 GB in eighty seconds. It killed a CI job during #124.
- **[#103](https://github.com/lydite/lydite/issues/103)** — `lydite mutation` has no job in
  `lydite/actions`.
- **The memory store has a note that #124 made stale.**
  `pullrequestfromref-also-accepts-head` records that `pullRequestFromRef`'s doc and tests omit
  the `head` ref; both now cover it. You do not hand-edit the store — run the curation pass.

## Traps previous sessions sprung

- **A `go test -run` that matches nothing prints `ok`.** **Always confirm a new test actually
  ran** (`-run '<Name>' -v`, and read the `=== RUN` lines).
- **Two `sed` expressions apply in sequence to the same line.** When you transform a file
  mechanically, rebuild the original from the output and diff it.
- **`golangci-lint` skips analysis using cached facts**, then reports `0 issues` having run
  nothing. A planted defect is the only way to know the check is live.
- **A mutant that drops a loop's decrement turns an appending loop unbounded**, and the runner
  dies of memory rather than of the timeout — the job exits 143 with lydite reporting
  "interrupted", which looks nothing like a mutation failure. That is #109. **Prefer forward
  iteration and `range` over a manual index**: it leaves no step to drop.
- **`jq`'s `contains` raises on a null body.** `(.body // "")` first.
- **A `npx vitest run --coverage` leaves `source/cloud-services/coverage/`** in the tree, and
  the next `lydite scan` fails on Semgrep findings inside the generated `prettify.js`. Delete
  it before scanning.
- **Editing a `note:` in `.gt-repo.yaml` re-renders `.github/dependabot.yml`.** `gt repo check`
  fails until you run `gt repo sync`. Never hand-edit the generated file.
- **Polling `gh run list --limit 1` right after a push reads the previous run.** Poll by commit
  SHA (`--commit "$(git rev-parse HEAD)"`).
- **`gh pr checks` exits non-zero when any check is not green**, so `s=$(gh pr checks …) || …`
  swallows every poll.
- **The Bash tool caps a foreground command at ten minutes.** A CI poll longer than that is
  moved to the background and keeps running; a handful of them pile up and have to be killed
  by hand. **Check CI once, report, and move on** — do not sit in a polling loop.
- **The `referral` job exits 0 on a referral.** `lydite/referral` stays *pending* until a human
  comments `/lydite clear`, and `.lydite/exemptions.yml` does not exist — so **every** pull
  request here is referred. A green job says nothing about the verdict; read the commit status.
- **Pushing cancels the in-flight run** — `lydite-pr.yml` sets `cancel-in-progress`.
- **Correlated reviewers reach the same wrong answer.** Run duplicate bug-hunters independently
  and treat convergence as evidence, not proof — and **verify a claimed platform behaviour
  against the platform, not against a forum summary.** Three of #124's design decisions were
  wrong until a scratch review on a real pull request settled them.
- **A fix applied to two of three copies reads as done.** #124 hardened the marker match in the
  CLI and the relay, wrote prose saying the rule held everywhere, and left the composite
  action — the only path in force — untouched. A panel caught it. When you change one of a set,
  grep for the set.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.
- **`git reset --hard` is blocked by the permission classifier.** Move a branch with
  `git branch -f <name> <sha>` from another branch instead.

## What the platform actually does, settled empirically

Verified on a scratch review during #124. Do not re-litigate these from documentation.

- A review with `comments[]`, `event: COMMENT` and **no `body`** is accepted.
- A review's comments are `DraftPullRequestReviewComment`: **no `subjectType` field, and
  `position` may not be null.** A file-anchored claim is refused inside a review (422) and
  accepted on `POST /pulls/{n}/comments` with `subject_type: file` and an explicit `commit_id`.
- Deleting a comment another identity authored answers **403** where the identity can see it
  and **404** where it cannot. A reply that is itself 404 means the comment is gone.
- A file-level comment comes back with a non-null `position`, so `subject_type` has to be read
  alongside it to tell an outdated thread from a file one.

## Environment

- **SSH is blocked.** `origin` is an SSH alias, so plain `git fetch`/`push` fail with
  `Permission denied (publickey)` — and a failed `git fetch origin main` leaves `FETCH_HEAD`
  stale, which has already caused a cherry-pick to land on the wrong branch. Always check the
  fetch succeeded. Push and fetch over HTTPS with the gh credential helper:

  ```sh
  git -c credential.helper='!gh auth git-credential' \
      push https://github.com/lydite/lydite.git <branch>:<branch>
  ```

- **When a lydite command shells out to git itself** — `mutation`, `test --gate-coverage`,
  `review`, `scan --diff-base auto` all run `git fetch origin <branch>` in a child process —
  `git -c` cannot reach it. Pass the rewrite through the environment:

  ```sh
  export GH_TOKEN="$(gh auth token --user pedromvgomes)" \
    GIT_CONFIG_COUNT=2 \
    GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf GIT_CONFIG_VALUE_0=git@github-personal: \
    GIT_CONFIG_KEY_1=credential.helper GIT_CONFIG_VALUE_1='!gh auth git-credential'
  ```

  Without it the command stops at `mutation is scoped to the change against the merge-base, and
  it could not be resolved: fetch origin main: exit status 128`, which names a shallow checkout
  as the usual cause and is misleading here.
- **No container runtime**, so nothing touching the proving ground runs locally.
- `lydite scan` runs from the repository root (`--dir ../..` from `source/cli`). `--dir ..`
  names `source/`, which declares no components and errors.
- A full `go test -race ./...` is roughly four minutes of compile and then the suite;
  `lydite test --component cli` is about four minutes, and `lydite mutation --component cli`
  was **seventy-eight minutes** on #124's diff. Budget for it, and run it in the background.
- A second gh account, `pedrogomes-td`, has **read** access to `lydite/lydite`. It is what made
  the cross-identity delete probe possible.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. `CLAUDE.md` says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to. **Never tag a release, and never move `@v1`.**
- Comments describe the code as it is — no "used to", no "now", no ticket numbers, no roadmap.
  ADRs are the exception. Test names say what behaviour they protect.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the grammar tags**,
  from `source/cli`, plus `lydite scan --dir .` and `gt repo check` from the root — and
  `lydite test --component cli` to see the CRAP row, because a gate can fail for a reason the
  change caused elsewhere.
- **Prove a regression test fails without its fix.**
- **Answer every mutation survivor**, and prefer rewriting past an equivalent mutant to
  declaring one. A boundary nothing can observe is answered by leaving no comparison to shift —
  a clamp states itself as `min`/`max`, a bounded walk as a `range` over a count.
- Delegate to the `memory-explorer` agent before answering *why* something is built the way it
  is, what breaks if you change X, or whether an approach has been tried.
- Invoke the `challenge` skill before `ExitPlanMode`. Review before proposing and again after
  acting — `/code-review` and then `/panel-code-review`, which caught real defects at every
  round on #124, including one the first pass missed. Keep going while rounds return findings
  that change behaviour, and stop when what comes back is wording.
