# PR 22 — `lydite mutation` resumes, and stops at a deadline

## Session prompt

> Make `lydite mutation` resumable. A run records every mutant verdict and its baseline as they
> land, keyed by a fingerprint of the exact tree it measured, and a rerun under the same
> fingerprint measures only what is missing. `--deadline` stops dispatch before a CI job timeout
> can discard everything, and answers `incomplete` — never a pass. The decision is
> [ADR 0075](../../docs/adr/0075-a-mutation-run-resumes-and-stops-at-a-deadline.md); read it
> first, then `agentic/references/mutation.md` and `architecture.md`'s "Mutation flows".

## Setup

- Branch: `claude/practical-hamilton-e9q8eu`, cut from `main`. Every slice is its own PR from
  its own branch, cut from `main` after the previous slice merges.
- Module root: `source/cli/`. The scan root is the repository root above it (`--dir ../..`).
- `agtk sync` is clean on this branch; the rendered rules in `.claude/rules/` apply.

## Slices, in landing order

This is **four pull requests**, each landable alone. Nothing a slice leaves behind is broken
while the next is unwritten: slice 1's state is invisible to a run that never reaches a
deadline, and slice 2's `--deadline` is off unless passed.

| # | Slice | Repo | Depends on |
|---|---|---|---|
| 0 | A declared `-timeout` reaches only the variants that run `go test` | lydite/lydite | — |
| 1 | Mutation state: tree digest, fingerprint, per-verdict writes, baseline cache, ADR + docs | lydite/lydite | 0 |
| 2 | `--deadline`, the `incomplete` outcome, fold and record handling | lydite/lydite | 1 |
| 3 | `actions/cache` wiring and a `deadline` input | lydite/actions | 2 released |

Phase 2 (the default-branch sweep) is decided in ADR 0075 and **not built** by any slice here.

### Slice 0 — a declared `-timeout` never reaches `go build`

**Problem.** `BuildOnly` derives its argv from the component's declared `args:` through
`goTestUninstrumented` → `dropCoverage` (`internal/runner/runner.go:513-556`), which strips only
the coverage flags. A component declaring `-timeout 30m` so its baseline can outrun `go test`'s
10-minute default therefore breaks `BuildOnly` (`go build -timeout` → `flag provided but not
defined`, reproduced). Resume does not fix this: a baseline that cannot run cannot be cached.

**Decisions.**
- Generalise the filter: `BuildOnly` drops every flag `go build` does not accept (the
  `go help testflag` set: `-timeout`, `-run`, `-skip`, `-count`, `-failfast`, `-short`,
  `-parallel`, `-cpu`, `-bench*`, `-benchmem`, `-fuzz*`, `-shuffle`, `-list`, `-coverprofile`
  family already covered), with a separately-passed value dropped with its flag. Flags both
  commands accept (`-race`, `-tags`, `-v`, `-json`, `-ldflags`, …) stay.
- `Plain` and `Instrumented` keep `-timeout`: it is how a component raises the baseline's bound.
- The coverage rule still holds: coverage flags reach only `Instrumented`.

**Tasks.**
1. *routine* — test-first. Add a failing case beside `TestTheUninstrumentedVariantsCarryNoCoverageFlag`
   (`runner_test.go:107`): a component declaring `-timeout 30m -run X` yields a `BuildOnly` argv
   without either (or their values), while `Plain` and `Instrumented` keep `-timeout 30m`. Then
   extend the filter. Files: `source/cli/internal/runner/runner.go`,
   `source/cli/internal/runner/runner_test.go`.
2. *routine* — declare `-timeout 30m` on this repository's own `cli` component in
   `.lydite/components.yml`, and add one sentence to `agentic/references/components.md` on how a
   component raises its baseline's bound. Files: `.lydite/components.yml`,
   `agentic/references/components.md`.

**Verify.** `go test -race ./internal/runner/...`; `go build ./...`; `golangci-lint run ./...`;
`go run ./cmd/lydite test --dir ../.. --component cli --no-coverage` still green.

### Slice 1 — mutation state

**Decisions (settled in the challenge; see ADR 0075).**
- **Tree digest** lives in a neutral package, `internal/treedigest`: a sha256 over the sorted
  `(path, contents)` of the file list `gitdiff.Tracked()` returns
  (`internal/gitdiff/gitdiff.go:184`, the same list `ScopeChange` hands `mutation.Tree` at
  `internal/stages/mutation/declaration.go:240`). Computed once per run.
- **Fingerprint** (per component) = hash of: tree digest, component name, the component's
  `runner.Invocation` `Name`+`Args` for the variants mutation runs, the provisioned toolchain
  versions, lydite's version, `--timeout`, `--memory`, `runtime.GOOS`/`GOARCH` (Darwin cannot
  bound memory, so an `OutOfMemory` verdict is platform-dependent), and a hash of the composed
  environment `in.Shape.Env(tc, c, t.inv)` hands the suite — names and values hashed, never
  printed. The base SHA is **not** in it. lydite's version is `main.version`, or for a `dev`
  build `dev+<sha256 of the running executable>`, so two local builds never share state.
- **Mutant id** = hash of path, operator, offset, length, original, mutated.
- **Store** — one directory per component: `<root>/<component>/baseline.json` and
  `verdicts.jsonl`, whose first line records the fingerprint. A mismatch truncates both. Default
  root `os.UserCacheDir()/lydite/mutation/<sha256 of the absolute scan root>/`; `--state-dir`
  (env `LYDITE_MUTATION_STATE`) replaces the whole root, with `<component>/` beneath it.
- **Writes** — one append per verdict, as it lands, serialised by a mutex over one `O_APPEND`
  file; no fsync (a process kill keeps what reached the kernel). A torn last line is ignored on
  read. Every store error is a diagnostic written to `Diagnostics` as it arises, never a failure.
- **Only a decided verdict is recorded.** A result the executor produced because the context
  was cancelled — `cutShort`'s "interrupted before this mutant was built" `Unviable`
  (`executor.go:290-292`), the cancelled-suite branch, and `Execute`'s own "ended before this
  mutant was built" placeholder (`executor.go:202`) — is never written. Otherwise the first
  Ctrl-C leaves those mutants `Unviable` for good under an unchanged fingerprint.
- **`MemoryUnbounded` is persisted** on each verdict line and on the baseline, so a reused run
  renders the same unbounded-memory note (`unboundedNote`, `cmd/lydite/mutation.go:843-856`).
- **Reuse** — every verdict kind is reused on a fingerprint match (TimedOut and OutOfMemory
  included: the cached baseline gives an identical budget). A baseline hit skips the baseline
  run and `coverage.Measure`; its record is pass/fail, elapsed, MaxRSS, `MemoryUnbounded`, and
  `Report.Executed`. Everything before the baseline in `mutateComponent` (`run.go:358`) —
  `Prepare`, `StartServices`, setup commands, slot acquisition (`run.go:370-389`) — still runs on
  a hit: the mutants need the services up, exactly as the baseline had them.
- **The state dir never enters the tree.** When the resolved state root lies under the scan
  root, `ScopeChange` (`internal/stages/mutation/declaration.go:240`) drops it from the file list
  it builds — the one list that feeds both the digest and `mutation.Tree`'s worker copy.
- **Default on**; `--fresh` discards the component's state before running.
- The row's detail says "N of M verdicts reused" whenever N > 0.

**Tasks.**
1. *routine* — `internal/treedigest`: `Digest(root string, files []string) (string, error)`,
   streaming, order-independent of input order. Tests: same contents → same digest; one byte
   changed, a file added, a file renamed → different. Files: `source/cli/internal/treedigest/*`.
2. *intricate* — the store in `internal/mutation`: `MutantID(Mutant) string`, `OpenState(dir,
   fingerprint) (*State, error)` returning the recorded verdicts and baseline on a match and
   truncating on a mismatch, `State.Record(Result)` (concurrency-safe), `State.SaveBaseline`,
   `State.Baseline`. Tests: round trip; mismatch truncates; a torn last line is skipped;
   concurrent `Record` from N goroutines yields N parseable lines. Files:
   `source/cli/internal/mutation/state.go`, `state_test.go`.
3. *intricate* — executor hook: `Options` gains `Known map[string]Result` and `Record
   func(Result)`. `Execute` returns known mutants' results without dispatching them, in the same
   order it would have, and calls `Record` for each fresh verdict the moment it is decided.
   Ordering ("own package first, then closure") must be unchanged for what is dispatched. Tests
   in `executor_test.go`: all-known dispatches nothing; half-known dispatches the other half;
   `Record` sees exactly the fresh ones; a run cancelled mid-way records none of its
   cancellation-produced results, and a resume re-dispatches those mutants. Files: `source/cli/internal/mutation/executor.go`,
   `executor_test.go`.
4. *routine* — inputs. New flow inputs in `internal/flows/mutation` (`StateDir`, `Fresh`,
   `LyditeVersion`), bound into `RunMutants`' `In`; the CLI supplies them from `--state-dir`,
   `LYDITE_MUTATION_STATE`, `--fresh`, and `main.version` (the `dev+<executable hash>` form for a
   `dev` build). No stage reads env or globals. `ScopeChange` excludes the state root from the
   file list when it lies under the scan root, and computes the tree digest once. Tests: flow
   builds with the new bindings; a state root under the scan root is absent from `Tree.Files`.
   Files: `source/cli/internal/flows/mutation/mutation.go`, `source/cli/cmd/lydite/mutation.go`,
   `source/cli/internal/stages/mutation/declaration.go` and its test.
5. *intricate* — `mutateComponent` wiring (`internal/stages/mutation/run.go`, from line 358):
   compute the fingerprint, open the state (`Fresh` truncates it), take the baseline from it on a
   hit (after `Prepare`/services/setup, which always run) or measure and save it, and pass
   `Known`/`Record` to `Execute`. Store errors go to `Diagnostics` as they arise. Tests in
   `run_test.go` with a fake backend: a second run under the same fingerprint dispatches nothing
   and yields the same counts; a changed file, `GOOS`, or composed env value re-runs everything;
   an unwritable state dir still measures everything and writes one diagnostic. Files:
   `source/cli/internal/stages/mutation/run.go`, `run_test.go`.
6. *routine* — reporting: `ComponentOutcome` gains `Reused int`; `kindRow` renders "N of M
   verdicts reused" in the detail when N > 0; a reused `MemoryUnbounded` still yields the
   unbounded note. Tests in `cmd/lydite`'s mutation tests. Files:
   `source/cli/internal/stages/mutation/run.go`, `source/cli/cmd/lydite/mutation.go` and its test.
7. *routine* — docs: finalise ADR 0075; add **Mutation state**, **Fingerprint**, **Incomplete
   run** and **Cleared file** to `CONTEXT.md` (glossary only, each contrasted with Ledger/Cache
   where relevant); a "Resume" section in `agentic/references/mutation.md`; a line in
   `architecture.md`'s "Mutation flows" for the new inputs. Files: those four.

**Verify.** `go build ./...`, the shipped-tags build, `go test -race ./...`,
`golangci-lint run ./...`. By hand: run `go run ./cmd/lydite mutation --dir ../.. --component
<small component>` twice; the second run reports every verdict reused and takes seconds. Edit
one file and rerun: nothing reused.

### Slice 2 — `--deadline` and `incomplete` (outline; tasks written when slice 1 lands)

- `--deadline <duration>`, measured from process start, threaded like `--timeout`. At the
  deadline: stop dispatch, cancel in-flight mutants, flush state.
- A deadline and a Ctrl-C are both context cancellation today (`out.Interrupted =
  ctx.Err() != nil`, `run.go:252`). The deadline cancels with its own cause
  (`context.WithDeadlineCause`), and the stage tells them apart with `context.Cause`: a deadline
  answers `KindIncomplete`, a Ctrl-C keeps today's interrupted path.
- New `KindIncomplete` in `run.go` carrying measured/wanted. `kindRow` renders `unmeasured`
  ("N of M measured, rerun to resume"), or `fail` when a survivor was already found.
- `unmeasured` does not vote (`internal/ui/report.go:103-129`), so the CLI forces a non-zero exit
  whenever any component is incomplete, under `--no-gate` too.
- `mutants.json`'s `ComponentCounts` gains `Incomplete *struct{Measured, Wanted int}`; `mutation
  merge` renders it as above; `lydite test record` skips an incomplete component, as the
  interrupted path (`withdrawInterrupted`, `cmd/lydite/mutation.go:637`) already withdraws one.

### Slice 3 — `lydite/actions` (outline)

- The mutation action gains `state-dir` (default `${{ runner.temp }}/lydite-mutation-state`,
  **outside** the checkout) and `deadline` inputs.
- `actions/cache/restore` with key `mutation-<component>-<sha>-<run_attempt>` and restore
  prefix `mutation-<component>-<sha>-`; `actions/cache/save` under `if: always()`.
- Every expression reaches `run:` through `env:` (rule: no interpolation in run bodies).

## Gates

Before every push: `go build ./...`, the `grammar_subset` build from `CLAUDE.md`,
`go test -race ./...`, `golangci-lint run ./...` — all from `source/cli/`.

## Loop A — review before the PR

`review-implementation` on the slice until a pass comes back clean or the cap is reached.

## PR opening

`open-pr`. Conventional title, e.g. `fix(runner): keep go-test-only flags out of go build`
(slice 0), `feat(mutation): resume a run from its recorded verdicts` (slice 1). No PR is opened
unless the user asks.

## Loop B — PR review

Resolve every thread; re-run the gates after each push.

## Completion

A slice is done when its PR is green, mergeable and reviewed. Slice 1 is not done until a
second run of an unchanged component reuses every verdict, by hand, as well as in tests.

## Owed

- Phase 2 sweep: its own plan when it is picked up (ADR 0075 fixes its guarantees).
- `lydite test` adopting `internal/treedigest` only if a suite comes to outrun its job.

## Traps and non-negotiables

- **A state dir inside the checkout poisons its own fingerprint.** An untracked, non-ignored
  directory under the scan root is in `gitdiff.Tracked()`'s list, so every appended verdict
  changes the digest the next run computes. The digest and the worker copy both exclude the
  resolved state dir when it lies under the scan root; slice 3 puts it under `runner.temp`.
- The state is a **Cache**: no store error may fail a run or change a row's status.
- No stage reads the environment, a global or `main.version` — they arrive as flow inputs.
- Store diagnostics go to `Diagnostics` as they arise, not into `Out`.
- An `incomplete` component is never a pass and never reaches the ledger.
- No cgo; comments describe the code, not its history.
