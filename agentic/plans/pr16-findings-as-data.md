# Session prompt — findings as data, and the threads that carry them

Continues `pr15-the-ledger.md`, which shipped
[#113](https://github.com/lydite/lydite/pull/113) and closed
[#26](https://github.com/lydite/lydite/issues/26). Where the two differ, this file wins.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. **Work in one worktree** — do not
create another. Branch from `main`.

**Verify before you branch.** At the time this was written #113 was open and mergeable, held
only by `lydite/referral` pending a human `/lydite clear`; `main` was `dce1bd7`. So:

```sh
git fetch origin main
git log --oneline -1 origin/main            # must be the ledger commit, not dce1bd7
git show origin/main:source/cli/internal/ledger/ledger.go >/dev/null   # must exist
git checkout -b <type>/<name> origin/main
```

If `internal/ledger` is not on `main`, #113 has not landed. **Stop and say so** rather than
branching off a base that does not contain the ledger — everything below assumes it does.

The root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument and use it for
every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above it. Every `go`
and `golangci-lint` command runs from `source/cli` with `GOTOOLCHAIN=local`, **and with the
release build tags**:

```sh
GOTOOLCHAIN=local go test -race -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

Without them the binary carries all 206 of gotreesitter's grammars, and `ci-test` runs the
suite *with* them — a language whose own `grammar_subset_<lang>` tag is missing fails at its
first parse, and a bare `go test` can never see it.

## The gate on the release

**Nothing ships to consumers until secrets ([#20](https://github.com/lydite/lydite/issues/20)),
licences ([#21](https://github.com/lydite/lydite/issues/21)) and flaky tests
([#22](https://github.com/lydite/lydite/issues/22)) are done.** CRAP (#16) and the ledger
(#26) are off that list. #17 remains open and is research.

`lydite/actions` `@v1` still points at `e936433` — the pre-shard commit — so consumers run the
old serial `test` job against the v0.1.0 binary. That pairing is consistent and unbroken. **Do
not move `@v1`, and do not tag a lydite release.** The order, when it happens, is fixed: a
lydite release shipping `test plan`/`test merge`/`test record` first, because `setup@v1`
resolves `latest`; only then does `@v1` move. Reversed, every consumer calls `lydite test plan`
on a binary that has no such command.

## The slice

**[#111](https://github.com/lydite/lydite/issues/111) and
[#114](https://github.com/lydite/lydite/issues/114), designed together.** They share one
missing piece, and inventing it twice is the failure this pairing exists to avoid.

Today `ui.jsonRow` carries `Detail []string` — prose. A per-site finding's location survives
only as rendered text, so nothing downstream can anchor it, count it, or track it. AGENTS.md
forbids the obvious shortcut in terms: *"Do not reintroduce a bracketed text mode to
accommodate it. A text-scraping consumer forces every refinement to the human surface through
a synchronised release."*

So the slice is **findings as data in the report document**, and then the two things that
consume it:

- **#111** — every scanner emits a structured report lydite renders findings from, as it
  already does for Biome alone, and the ledger records the count.
- **#114** — a finding that names a file and a line becomes a review thread on that line.

**Not the dashboard** ([#27](https://github.com/lydite/lydite/issues/27)). `source/web/` is
still empty.

### What is already established, and needs no rediscovering

- **Enforcement is on.** `gt repo config` resolves
  `settings.branch_protection.require_thread_resolution: true`, so an unresolved thread
  already blocks a merge here. This does **not** hit the wall
  [#34](https://github.com/lydite/lydite/issues/34) hit — that one is blocked because gt
  hardcodes the required *check* to `ci-gate`, which is a different field.
- **An identity that can post.** The relay mints `pull_requests: write` (ADR 0022), which
  covers `POST /repos/{o}/{r}/pulls/{n}/reviews`. Resolving a thread is GraphQL
  `resolveReviewThread` and it is **unverified** whether the App's grant reaches it. Check
  that first: if it does not, the whole resolve-and-reopen half needs a different answer and
  the slice is shaped differently.
- **The locations exist.** `crap.Function` carries `File` and `Line`; `mutation.Mutant`
  carries `Path`, `Line` and `Column`. Patch coverage's uncovered changed lines are already
  computed and simply never surfaced per line. Only scan findings are unparsed.

### Decide these before writing anything

1. **The shape of the findings channel.** It has three consumers — #111's counts, #114's
   threads, and ADR 0009's deferred per-finding fingerprints. Design it once for all three.
   `ui.Document` accepts unknown keys and `ui.jsonRow` is deliberately a separate type from
   `ui.Row`; decide whether findings hang off a row or sit beside the rows, and say why.
2. **Fingerprints, which are the hard part.** A line number is not an identity: an edit above
   a finding moves it, and the next run posts a duplicate beside an unresolved original. It
   has to be content-derived — the component plus the function's name for CRAP, the operator
   plus the replaced text plus its enclosing function for a mutant. Decide what a scanner
   finding's is, where the tools disagree about everything but the rule id.
3. **Whether lydite renders findings, or the tool does.** #111 means `gosec -fmt json`,
   `clippy --message-format json`, `cargo-audit --json`, `semgrep --json`, each parsed and
   each rendered by lydite rather than streamed by the tool. That reverses the stance
   `executil.Run` exists for — *"a scanner's findings are the point"* — and it is a real
   cost, not a formality. Decide it out loud.
4. **Thread lifecycle.** Resolve when the finding is gone, with a reply saying which run
   resolved it; reopen rather than duplicate when it returns. Decide what happens to a thread
   whose finding moved to a different line but kept its fingerprint.
5. **Whether the standing comment keeps listing what now has a thread.** ADR 0023 says there
   is exactly one surface. Two surfaces listing the same finding is duplication; a comment
   that stops listing them loses the at-a-glance count. Decide, and amend ADR 0023 — the
   argument for a second surface is that a standing comment cannot be resolved per finding,
   cannot anchor to a line, and keeps no record of what was done about any one of them.

**A new storage or surface policy needs an ADR.** The rejected alternatives are the decision's
content.

## Owed from the last session

- **`docs/release-notes/v0.2.0.md` is on `main` and stale.** It mentions mutation zero times;
  it names `.lydite-reports/baseline.json`, which is `measurements.json`; it has no `lydite
  test plan`, `test merge` or `test record`; its closing `lydite/actions` section reads as a
  to-do for work that has merged; and it now also lacks the CRAP gate, the
  `[lydite:exclude_from_<gate>]` grammar, ADR 0028, and the whole quality-history ledger with
  ADR 0029. It must be correct **before** a tag is pushed — `release.yml` reads it out of the
  tagged tree — and the release is held, so there is time. **Its own pull request.**
- **`.agents/plans/pr15-the-ledger.md` and this file are untracked.** Plans land in their own
  `docs(plan):` pull request, never with the feature.
- **[#112](https://github.com/lydite/lydite/issues/112)** — mutation scalars in the ledger.
  Filed, unstarted, and structurally blocked: mutants come only from lines the change touched,
  and on the default branch HEAD is its own merge-base, so the one job holding a token that
  can push mutates nothing. It needs a route from a pull request's measurement to the branch
  that does not hand a branch's own code a writable token, which is #49.
- **[#109](https://github.com/lydite/lydite/issues/109)** — a mutant is bounded in time and
  not in memory. Measured: 14 GB in eighty seconds against a timeout that fired correctly at
  1m46s.
- **[#103](https://github.com/lydite/lydite/issues/103)** — `lydite mutation` has no job in
  `lydite/actions`, so even after the cutover consumers get no mutants.
- **[#97](https://github.com/lydite/lydite/issues/97)** — 159 mutants in ~15m on `cli` in CI
  is the current datapoint.

## Traps this session sprung

- **A gate can fail for a reason the change caused elsewhere.** Answering the review's
  findings introduced a function at CRAP 56.0 with no test, which failed a gate that had been
  green. Re-run the gates after fixing anything, not only after writing the feature.
- **A test can pass on an absent row.** A zero `ui.Row` has status `""`, which is not `"fail"`
  — so `if row.Status == "fail"` passes when the row was never added. Assert what a row *is*,
  never only what it is not.
- **A test can cover a reader it never reaches.** A long-line test exercised the partition
  reader and not the projection reader, because the second `Append` short-circuits at
  `Recorded` before `project` runs. The mutant was the only thing that noticed.
- **Prefer deleting an unreachable branch to testing it.** `gapBefore` had a case
  `CommitsBetween` can never produce, because the early return above already took it.
- **A value returned beside an error must be the zero one.** Three functions returned
  plausible ones; nothing observed it because every caller checks the error first, and the
  contract still breaks for the next caller.
- **Verify a mutant by applying it.** Two independent reviewers agreed, confidently and with
  reasoning, that `cargo llvm-cov` redirects nextest's store so the JUnit path was wrong. It
  does redirect the build target dir — and `target/` holds *both* `llvm-cov-target` and
  `nextest`, so the path was right. Correlated agents reach the same wrong answer.
- **`gh pr checks` exits non-zero when any check is not green**, so `s=$(gh pr checks …) || …`
  swallows every poll.
- **Pushing cancels the in-flight run** — `lydite-pr.yml` sets `cancel-in-progress`.
- **The `referral` job exits 0 on a referral.** Read `review referred in …` in the step's own
  output; a green job says nothing about the verdict. `lydite/referral` stays *pending* until
  a human comments `/lydite clear`, which is what held #113.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.

## Environment

- `gh auth setup-git`, then push over **HTTPS** with the gh credential helper:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/lydite/lydite.git <branch>:<branch>`.
  SSH is blocked here, and a token must never reach a remote URL.
- **No container runtime here**, so nothing touching the proving ground runs locally.
- `lydite scan` runs from the repository root (`--dir ../..` from `source/cli`, or `--dir .`
  from the root). `--dir ..` names `source/`, which declares no components and errors.
- The `cmd/lydite` suite is now slow: each end-to-end `record` test drives a full instrumented
  suite through gotestsum. A full `go test -race ./...` is roughly fifteen minutes.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to. **Never tag a release, and never move `@v1`.**
- Comments describe the code as it is — no "used to", no ticket numbers, no roadmap. ADRs are
  the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the grammar tags**,
  from `source/cli`, plus `lydite scan --dir .` and `gt repo check` from the root.
- **Prove a regression test fails without its fix.**
- **Answer every mutation survivor**, and prefer rewriting past an equivalent mutant to
  declaring it.
- Review before proposing and again after acting. Keep going while rounds return findings that
  change behaviour, and stop when what comes back is wording — and say so.

## The rest of the held list, in the order I would take it

- **#111 + #114** — this.
- **[#20](https://github.com/lydite/lydite/issues/20)** — secrets. Independent, and the ADR
  0006 shape is well-trodden: manifest, `dependabot.yml` entry, `.lydite/components.yml`
  exclude. Note the issue's last line — deleting a committed secret does not remove it from
  history, and the finding should say so rather than implying the fix is complete.
- **[#21](https://github.com/lydite/lydite/issues/21)** — licences. Needs a policy key in
  `.lydite/config.yml`, which is the one place a repo-level fact belongs. Delta, like CRAP.
- **[#22](https://github.com/lydite/lydite/issues/22)** — flaky tests. Independent, and it
  interacts with mutation: a flaky test makes mutation results meaningless, so it arguably
  gates mutation's inputs.
- **[#17](https://github.com/lydite/lydite/issues/17)** — the research. Longest pole, and the
  honest answer may be a hand-rolled cyclomatic walk per language.
