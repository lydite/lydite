# Session prompt — what a consumer actually gets

Continues `pr12-the-mutation-probe.md`, which shipped
[#101](https://github.com/lydite/lydite/pull/101) and closed
[#95](https://github.com/lydite/lydite/issues/95). Where the two differ, this file and
[ADR 0026](../../docs/adr/0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md)
win.

This slice is [#90](https://github.com/lydite/lydite/issues/90) and
[#87](https://github.com/lydite/lydite/issues/87), and **most of it is in a different
repository**. Read the section on that before planning anything.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. Branch from `main`, currently
`17b23ee`. The root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument
and use it for every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above it. Every
`go` and `golangci-lint` command runs from `source/cli` with `GOTOOLCHAIN=local`, **and
with the release build tags**:

```sh
GOTOOLCHAIN=local go test -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

Without them the binary carries all 206 of gotreesitter's grammars, and `ci-test` runs the
suite *with* them — a language whose own `grammar_subset_<lang>` tag is missing fails at
its first parse, and a bare `go test` can never see it.

## What #101 established, and what it did not

`ci-end2end.yml`'s `proving ground — mutation` job plants two identical functions per
component in Go, Rust and TypeScript, and requires each component to survive exactly half
of what it scored. It passed on its first CI run, and the numbers are worth carrying:

| | tally (Rust) | sdk (Go) | web (TS) | whole job |
|---|---|---|---|---|
| elapsed | 11s | 9s | 35s | **1m 30s** |

Cold `~/.cargo` and `~/.npm`, a real postgres stack for `tally`, 12 of 24 mutants killed.
`timeout-minutes: 60` was never within an order of magnitude of being reached.

It also fixed two defects it found before it ran once: a worker directory held the
component's own files alone, so a component whose build reads a file above itself compiled
no mutant at all and reported a green `unmeasured`; and the copy was the one write path in
`internal/mutation` not confined by its `os.Root`, so a listed path beneath a committed
symlink truncated a file outside the worker.

**None of that reaches a consumer.** `lydite/actions` ships `setup`, `review`, `scan`,
`test` and `publish`, its `lydite.yml` runs one unsharded `test` job over every component,
and it was last pushed on 2026-09-04 — before the shards, before `test record`, before
`lydite mutation` existed at all. A consumer pinned to `@v1` gets a single serial test job
and no mutation whatsoever. Four merged pull requests of work is invisible to everyone but
this repository.

## The slice

**#90 and #87 together, in `lydite/actions`.** They are siblings and one pull request can
close both: #87 adds the `record` step and the write-token split, #90 adds the
plan/matrix/merge shape around it. Both issues say the same thing about timing — they are
cross-repository cutovers and both should land before a release carrying them reaches
consumers through the action.

`.github/workflows/lydite-pr.yml` and `.github/workflows/lydite-baseline.yml` in *this*
repository are the shape to package. Read them first; they are the reference
implementation and they already work.

## Scope

- A `plan` job reading `.lydite/components.yml` and each component's compose file and
  nothing else — no git, no network, no process — writing the matrix to `--out` and
  exposing it as a job output.
- A `test` matrix over `fromJSON(needs.plan.outputs.shards)`, `fail-fast: false`, each job
  passing `--component "$COMPONENTS"`; on a pull request also `--affected`, and **never on
  a push**, where ADR 0016 requires the run to be complete.
- A `merge` job over every shard's report directory.
- Artifacts named so `publish` cannot read a shard's document as a `test` section of its
  own: `lydite-shard-<name>`, leaving `lydite-reports-*` for what `publish` reads.
- The `record` step, folding the same directories in one write after the fold rather than
  one push per shard.

## The third gap, which has no issue yet

**`lydite mutation` is not in `lydite/actions` at all** — no action, no job in the reusable
workflow. That is not tracked anywhere, and it is not this slice.

**File the issue before writing any code**, and do not let it ride along in the #90/#87
pull request: a PR closes the issues it names, and scope that arrives unannounced is scope
nobody agreed to. The issue should say what `lydite-pr.yml` already demonstrates — that the
mutation matrix reuses `plan`'s output verbatim rather than running after `test`, because a
shard is the same conflict closure whichever command consumes it, and mutation shares a
checkout with the coverage gate and not a compilation.

## Owed from the last session

- **Comment on [#97](https://github.com/lydite/lydite/issues/97).** pr12 said to comment
  either way and the comment was never posted. The content is now better than it was: the
  probe gives a second cost datapoint at the opposite end of the scale from #96's 54
  minutes — 24 mutants across three languages in 54 seconds, cold. Per-mutant cost is
  roughly flat across both, which says the 54 minutes was the mutant count and nothing that
  degrades with diff size. Do not close it.
- **`actions/setup-go`'s `cache: true` is doing nothing in all three `proving ground` jobs.**
  The log says `Restore cache failed: Dependencies file is not found in
  /home/runner/work/lydite/lydite. Supported file pattern: go.mod` — `go.mod` is at
  `source/cli/go.mod`, and the step has no `cache-dependency-path`. Every one of those jobs
  re-downloads the module graph. It costs a slower run and never a red one, which is why
  nobody has noticed. One line each; fix it in passing or file it, but do not silently
  leave it.
- **Three reviewers died mid-review on #101** (`general/correctness-A`,
  `general/correctness-B`, `general/tests`, all on a session rate limit). The workflow
  half of that change — the heredoc mechanics and the `--mutation` regexes — merged with
  my own verification and one standalone reviewer's behind it, and no independent panel
  pass. CI passing on the first run is real evidence, not a substitute. If something there
  turns out wrong, that is where to look.

## Traps this session sprung

- **A green `lydite mutation` and a green assertion are different claims.** The assertion
  reads the report document's own `exit` field, which is what `ui.Report.ExitCode`
  rendered; CI reads the process's status. A `main.go` that stopped translating a
  `ui.ExitError` would leave every row red, the document saying 1, and the gate passing.
  The probe now checks both. Any new assertion owes the same distinction.
- **Prove a regression test fails without its fix.** Both tests added in #101 were run
  against the unfixed code first and observed to fail. A test written after a fix and never
  seen red is a test that asserts the fix compiled.
- **`status` is read-only in zsh and ordinary in bash.** A workflow step's `status=0; cmd
  || status=$?` idiom cannot be rehearsed in this shell without `bash -e <<'BASH'`.
- **The `referral` job exits 0 on a referral.** It is `lydite review --publish ||
  verdict=$?` and only re-raises above 2, so a green job says nothing about the verdict.
  Read `review referred in …` in the step's own output.
- **Pushing cancels the in-flight run.** `lydite-pr.yml` sets `cancel-in-progress`.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.

## Environment

- `gh auth setup-git`, then push over **HTTPS** with the gh credential helper:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/lydite/lydite.git <branch>:<branch>`.
  SSH is blocked here, and a token must never reach a remote URL.
- **`lydite/actions` is not in this worktree.** Clone it into the scratchpad to read and to
  work in; its change is its own pull request in its own repository, and it cannot merge
  before a lydite release ships the commands it calls.
- **No container runtime here**, so nothing touching the proving ground runs locally. A
  reusable workflow is validated by a consumer running it, which makes each push expensive
  — get the shape right by reading `lydite-pr.yml` rather than by iterating on red jobs.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no ticket numbers, no roadmap. ADRs
  are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- **Never interpolate `${{ }}` into a `run:` body.** The shard's component list arrives
  through an `env:` block. Semgrep's `yaml.github-actions.security.run-shell-injection` has
  caught that exact mistake in `lydite/actions` twice.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the grammar
  tags**, from `source/cli`, plus `lydite scan --dir .` and `gt repo check` from the root —
  for any change that lands *here*. A change in `lydite/actions` runs that repository's own
  checks.
- Review before proposing and again after acting. The review after acting found a confirmed
  path-traversal write in #101 that a full day of care had not; keep going while rounds
  return findings that change behaviour, and stop when what comes back is wording — and say
  so.

## If you would rather stay in this repository

Three in-repo slices, in the order I would take them:

- **[#57](https://github.com/lydite/lydite/issues/57)** — coverage counts components no run
  could ever measure. Directly adjacent to the `unviable`/`unmeasured`/does-not-vote
  reasoning #101 spent its whole budget on.
- **[#89](https://github.com/lydite/lydite/issues/89)** — a provisioned toolchain records
  its manifest text as the producer rather than the version it installed, so a baseline
  compares equal across a real change of instrument.
- **[#55](https://github.com/lydite/lydite/issues/55)** — the Rust toolchain probe asks
  cargo its version rather than rustup what is installed, so a component pinning an older
  channel is never materialised and rustup fetches it lazily mid-`clippy`, without the
  components the checks need.
