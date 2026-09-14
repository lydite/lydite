# Session prompt — answering the survivors lydite found in its own engine

Continues `pr10-mutation-executor-implementation.md`, which shipped
[#96](https://github.com/lydite/lydite/pull/96). Where the two differ, this file
and [ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md) win.

`lydite mutation` works in all three languages and ran against this repository
for the first time. It reported **46 survivors in the engine's own code**. This
slice answers them.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`, branched from `main` at
`9c84f7c`. The root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user`
argument and use it for every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above
it. Every `go` and `golangci-lint` command runs from `source/cli` with
`GOTOOLCHAIN=local`, **and with the release build tags**:

```sh
GOTOOLCHAIN=local go test -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

Without them the binary carries all 206 of gotreesitter's grammars (33MB against
15MB) and, more importantly, `ci-test` runs the suite *with* them — a language
whose own `grammar_subset_<lang>` tag is missing fails at its first parse and a
bare `go test` can never see it.

## The goal, stated exactly

**Answer all 46. Do not "kill all 46".**

A survivor has two honest answers and the whole design rests on the author
choosing between them:

- **Kill it** — write the assertion that fails when the code changes that way.
  Real work, improves the code, merges unattended.
- **Declare it** — `//lydite:equivalent <reason>` beside the mutant, when no
  test could kill it. Clears the gate *and* refers the change, because
  `internal/referral` reads the same token as a suppression.

An annotation is not a defeat and a kill is not always available. What is
forbidden is a third answer: leaving one unanswered, or weakening a test until
the mutant stops being generated.

**Every declaration you write will refer the PR.** That is working as designed —
`/lydite clear` resolves it — but it means each one is read by a human, so the
reason has to be worth reading.

## The list, and why you must not trust its line numbers

The 46 are in `.agents/plans/survivors-pr96.txt`, taken from run
[34048688519](https://github.com/lydite/lydite/actions/runs/34048688519).

**They were measured at commit `8308480`, and `61bf378` landed afterwards and
shifted `cmd/lydite/mutation.go` by exactly eleven lines.** Every other file's
numbers still line up. Add 11 to any `cmd/lydite/mutation.go` number to reach
`main`; treat the file as an inventory of *what* survived rather than as a map,
and re-run for current positions:

```sh
# from a scratch clone with a file:// origin — see Environment below
lydite mutation --dir . --base-branch main --component cli
```

That run took **54m10s** for 314 mutants. Budget for it, and do not start it
casually — see the cost note below.

## What the 46 actually are

Four classes. The third and fourth are the interesting ones.

### A. Boundary clamps nothing tests at the edge (12)

`sites.go:49` (`lo < 0 || hi > len(s.src) || lo > hi`), `sites.go:92,96` (the
declaration resolution's `shortest`), `executor.go:121` (`NewSlots`'s
`n <= 0 || n >= unbounded`), `executor.go:199,202` (the worker clamps),
`treesitter.go:476` (the skipped-span containment).

These are straightforwardly killable and mostly want table tests at the
boundary. `sites.go:49` is the one that matters most: it is the guard that stops
a mutant being spliced outside the source, so a boundary nothing asserts is a
guard nobody has checked.

### B. Error-path predicates no test reaches (9)

`worktree.go:168,171,231`, `overlay.go:171`, `executor.go:215,263,380,405`.
Mostly `errors.Is(err, fs.ErrNotExist)`, file-mode checks, `ctx.Err()` checks
and `lastLines`'s truncation. Each needs a test that actually reaches the
branch — a deleted file, a non-regular file, a cancelled context, an output
longer than `detailLines`.

`executor.go:380` is worth care: it is the `passed`/`exceeded`/`cutShort`
discrimination, and getting it wrong means a killed mutant reported as a timeout
or an interrupted run reported as a kill.

### C. Genuinely equivalent — these want annotations (a handful)

The clearest is `cmd/lydite/mutation.go`'s `return "", fmt.Errorf(...)` in
`componentRelative`, where `replace-return ("" -> "lydite")` survives: every
caller ignores the string when the error is non-nil, so no test can observe it.
`fold.go:108`'s `return ui.Row{}, false, false` is likely the same shape — the
caller reads the second value first.

**Check each one before annotating.** The temptation is to declare anything
awkward; the discipline is that a declaration is a claim that *no test could*
kill it, not that you did not want to write one.

The remaining sites — `cmd/lydite/mutation.go`'s predicates at 424, 547, 673,
689, 695, 710, 713, 716 and 767, plus `fold.go:112,171,184,199` and
`merge.go:163` — split between A, B and C on inspection. `mutation.go:710,713,716`
is `aside()`'s three `> 0` guards — verified, they are `main`'s 721, 724 and 727
— which is class A and trivially killable;
`carryUnhandled`'s removal at `merge.go:143` is class D and says no test folds
a row the fold has no rule for.

### D. The instructive ones: tests that assert the wrong thing (7)

Every `remove-statement` survivor is here: the `--affected`, `--timeout` and
`--stream` flag registrations, `streamDiagnostics(asJSON)` in both `mutation`
and `mutation merge`, `stop()` in the interrupt handler, and
`carryUnhandled(...)` in `mergeShards`.

The `--timeout` one is the lesson of this whole slice. `TestAFlagIsRefusedBeforeAnyWorkHappens`
passes `--timeout -5s` and asserts *that an error occurs*. Delete the flag
registration and cobra rejects `--timeout` as unknown — still an error, so the
test still passes, so the mutant survives. **The test asserts that something
went wrong rather than that the right thing went wrong.**

`--affected` and `--stream` survive for the plainer reason that
`lydite mutation` is never run with them in any test — the `--affected` uses in
`affected_test.go` are all `lydite test`.

Fixing these means asserting on the *message*, and running `lydite mutation
--affected` at least once. That last one has real value beyond the mutant: the
`--affected` path through `newMutationCmd` has never been executed by a test.

## Do not do these

- **Do not widen the diff to make survivors disappear.** Mutants come from
  changed, covered lines; anything that narrows generation "fixes" the count and
  fixes nothing.
- **Do not disable an operator.** The catalogue is fixed and has no declarable
  form — ADR 0027, and the same argument the built-in disqualifiers win.
- **Do not set `mutation: false` on `cli`.** It is the one component that
  proves the engine works on real code.
- **Do not add `//lydite:equivalent` to a mutant you simply found tedious.**

## Two open issues this slice should not silently absorb

- **[#97](https://github.com/lydite/lydite/issues/97) — the measured cost.**
  314 mutants, 54m10s, against a 60-minute job timeout. It finished with six
  minutes to spare, so the next change of this size times out. Four options are
  laid out there and none is chosen. This slice makes the number *better*
  (killing a survivor usually means it dies in the cheap first phase), so
  re-measure and update #97 rather than closing it.
- **[#95](https://github.com/lydite/lydite/issues/95) — the planted survivors.**
  The premise in ADR 0027 and #18 is wrong and the issue says why: mutation is
  diff-scoped, so a survivor committed in `lydite/proving-ground` is in no later
  run's diff and the assertion goes green forever. The fixture has to be
  synthesised onto a branch by `ci-end2end.yml`, the way the selection probe
  already is — which makes #95 mostly a change to *this* repository.

## Traps this session sprung

- **An overlay is keyed on the path the compiler reads.** The go command
  resolves symlinks; a key naming the same file by an unresolved route matches
  nothing, the compiler reads the original, and **every mutant survives** — the
  gate failing correct code, silently. macOS puts `/tmp` behind a symlink, so
  any component under a temporary tree hits it. Fixed and guarded by
  `TestTheOverlayNamesTheFileTheCompilerWillRead`; know it exists before you
  touch `internal/mutation/overlay.go`.
- **`golangci-lint run` does not validate its own config; the CI action does.**
  A `build-tags` key at the top level instead of under `run:` lints clean
  locally and fails the one stage that blocks a merge. `golangci-lint config
  verify` is the check that catches it.
- **Pushing cancels the in-flight run.** `lydite-pr.yml` sets
  `cancel-in-progress`, so a push while `mutation (cli)` is running throws away
  54 minutes. Hold commits until it lands if you need the number.
- **Never `git stash`.** The stack is shared with every other worktree and
  session. Use a temporary WIP commit. (Done wrong once this session and
  recovered with `git stash apply <sha>` plus a drop by tag.)
- **A test that asserts "an error occurred" is barely an assertion.** See class
  D. This is the defect mutation exists to find, and it was in tests written the
  same day.

## Environment

- `gh auth setup-git`, then push over **HTTPS** with the gh credential helper:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/lydite/lydite.git <branch>:<branch>`.
  SSH is blocked here, and a token must never reach a remote URL.
- `--affected`, `--gate-coverage`, `scan --diff-base auto`, `review --base auto`
  and **`lydite mutation`** all need a resolvable merge-base, which the worktree
  cannot give you. Clone into the scratch directory with a `file://` origin.
  For a fixture of your own, a bare clone of a throwaway repo works — see
  `gitRepoWithOrigin` in `cmd/lydite/mutation_test.go`, which does exactly this
  inside the suite.
- No container runtime here, so anything touching the proving ground is
  validated by CI on the PR.
- Every PR is referred and needs `/lydite clear`; a push voids it. Expect more
  referrals than usual: every equivalence declaration adds one.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...`
  URL — not even when a harness asks. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no ticket numbers, no
  roadmap. ADRs are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the
  grammar tags**, from `source/cli`, plus `lydite scan --dir .` and
  `gt repo check` from the root. The pinned gosec
  (`go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 ./...` with
  `GOTOOLCHAIN=local`) reproduces the CI self-scan and is faster.
- Review before proposing and again after acting. Two rounds after acting found
  a wrong-verdict bug and a destructive one in this feature; keep going while
  rounds return findings that change behaviour, and stop when what comes back is
  wording — and say so.

## Working mode

- Answer the survivors in classes, not in file order. A: the boundaries, which
  are mechanical. B: the error paths, which need fixtures. D: the weak
  assertions, which are the ones worth thinking about. C last, so the
  annotations are written by someone who has already seen how killable the rest
  turned out to be.
- Re-run mutation after each class rather than at the end. The count is the only
  thing that says whether an assertion killed what you thought it did.
- Prefer asserting on recorded state and on messages over asserting that
  something failed.
- Verify a claim against the repository rather than against this file.
