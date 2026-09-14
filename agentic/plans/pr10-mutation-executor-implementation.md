# Session prompt — building the mutation executor

Continues `pr10-mutation-executor.md`, which is the prompt the work began from.
Where the two differ, this file and
[ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md) win.

The design is settled and the benchmark is run. **No Go has been written yet.**
Six commits sit on the branch and every one is documentation.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/acequia-flash-flood`,
on `feat/mutation-executor`, branched from `main` at `6dedda4`. The root `.envrc`
scopes `GH_TOKEN` to a gh user — read the `--user` argument and use it for every
`gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above it,
where `.lydite/` lives. Every `go` and `golangci-lint` command runs from
`source/cli` with `GOTOOLCHAIN=local`; `lydite scan --dir .` and `gt repo check`
run from the root.

**Read [ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md) first, and
its "What the executor settled" section in full.** It is the record of a
`challenge` interview and a benchmark, and it answers every question the parent
prompt still lists as open. Do not re-open any of it, and do not re-run the
benchmark.

## What is settled, and must not be re-decided

- **Scope: all six steps, closing [#19](https://github.com/lydite/lydite/issues/19)
  and [#18](https://github.com/lydite/lydite/issues/18).** Keep the language
  backends behind one interface so Go, Rust and TypeScript are separately
  reviewable commits.
- **Rebuild, never in-process.** Go compiles to a native test binary and has no
  bytecode layer to rewrite in a live process. `go build -overlay` is why
  `Apply` returns bytes and never writes the tree.
- **Never pass `-count=1`.** Go's test cache is what makes a mutant cost 1.67s
  instead of 79s. An overlay invalidates the mutated package and its dependents
  and nothing else, so mutation gets incremental test selection for free. This
  is load-bearing and deserves a comment saying so where the invocation is built.
- **Two phases.** Run a mutant against its own package first; a kill there is
  final. Only a mutant that survives its own package is run against the full
  closure, because a library mutant is routinely killed only by its caller's
  tests. Costs a survivor nothing — the second run reads the first's cached
  result for the package they share.
- **Timeout derived from the measured baseline**, with a floor for suites too
  fast to measure, `--timeout` overriding. Without one `TimedOut` is an outcome
  nothing can produce.
- **A failed baseline suite is `unmeasured` and does not vote.**
- **`mutation: false` gets one `context` row**, so ADR 0026's completeness rule
  holds with no exception and the fold needs no second copy of the opt-out rule.
- **One concurrency bound.** A component stays one scheduler item, so its compose
  stack lifetime is unchanged; its mutants dispatch against the same limiter
  `--concurrency` sets. Serial-where-services is asked of `scheduler.Conflicts`
  with two of the component's mutants as items — they carry its ports so they
  conflict, and a component publishing none holds no directory either.
- **The fold emits a `mutation` summary row**, `context`, never `mutation(repo)`.
- **Worker directories are per concurrency slot and are confined with `os.Root`.**
  Not `safeJoin`, which is lexical — see the traps below.
- **`gotreesitter` is `github.com/odvcencio/gotreesitter`**, v0.52.0, MIT,
  verified cgo-free by build, grammars in-module under a commit-pinned lockfile
  refreshed weekly. Its incremental-reuse defects are closed and irrelevant here
  — lydite parses each file once and never edits a tree.

## What to build, in this order

Wire each piece to a caller as you go. The engine merged with no caller, and that
is how a defect survives a clean build, a clean lint and a passing suite.

1. **The executor**, behind a language-agnostic interface. Build-only first so an
   unviable mutant is told from a killed one, then the plain variant per mutant
   under the derived timeout, two-phase as above. Go's strategy is `-overlay`.
2. **`lydite mutation`**, a peer of `scan`, `test` and `review`. `--dir`,
   `--component`, `--affected`, `--base-branch`, `--concurrency`, `--timeout`.
   Writes `.lydite-reports/mutation.json`.
3. **`lydite mutation merge`.** ADR 0027 requires it and the sharded matrix in
   step 6 is unsound without it: each shard writes a partial document, and ADR
   0026's completeness rule — a declared component with no row is a failure — is
   what the fold enforces. Share one implementation with `lydite test merge`
   rather than writing a second that agrees today.
4. **`publish` gains a fourth section.** `concerns` in `cmd/lydite/publish.go` is
   a fixed `{command, title}` table; it needs one entry. ADR 0027 says "no new
   mechanism", which is true, and "no change", which is not.
5. **`UnmatchedDeclaration` reaches a human.** `GenerateGo` already reports the
   declarations that covered no mutant and nothing prints them. Stderr, named.
6. **Rust and TypeScript** through `gotreesitter`, behind the same interface,
   with worker directories. Plus the **golden-mutant test**: committed fixtures
   asserting the exact mutant set — offsets, operators, replaced text — so a
   dependency bump that moves a node boundary fails here rather than changing a
   consumer's verdict silently. This repository has no Rust component, so nothing
   else can see that change.
7. **The proving ground's planted survivors** — a function whose test calls it and
   asserts nothing, beside a properly tested neighbour, in each of the three
   languages, with `.github/assert-proving-ground.py` requiring the first to
   survive and the second to be killed. Second repository
   (`lydite/proving-ground`); a separate PR.
8. **CI**: a mutation matrix in `lydite-pr.yml` beside the test matrix, not after
   it, reusing `lydite test plan`'s output verbatim.

## The benchmark, so it is not run again

Measured on this repository, one machine, five distinct mutants per figure:

| | |
|---|---|
| per mutant, leaf package | 1.67s (0.55 build + 1.12 test) |
| per mutant, package imported by `cmd/lydite` | 76.2s |
| whole module, cache disabled | ~79s |
| `cmd/lydite` alone, uncached | 72.8s |
| killed mutant, two-phase | 2.36s |

Cost is **bimodal, not proportional to dependent count** — `annotation` has 5
dependents and `coverage` has 8, and they cost 1.67s and 76s. The discriminator
is whether `cmd/lydite` is in the closure, because that one suite is 73 of the
79 seconds.

## Traps this session sprung

- **A claim about the repository, asserted from memory of a doc comment, was
  wrong and nearly became a hole.** Both ADR 0027 and the parent prompt named
  `internal/download`'s `safeJoin` as the containment model for worker
  directories. `safeJoin` is seven lines of `filepath.Join` plus a `Clean` prefix
  compare and resolves no symlinks; it is sufficient where it stands only because
  that code separately rejects absolute link targets, containment-checks resolved
  relative ones, and unpacks into a directory it created rather than one it was
  handed. A worker directory is a copy of a scanned repository and holds symlinks
  nobody vetted. It survived a challenge interview, a write-up and a full review
  round. **Read the function, not the comment about the function.**
- **The first benchmark measured cache hits, not mutants.** The harness repeated
  the *same* overlay three times and reported the minimum, so runs two and three
  were served from the test cache and the number was ~40x too good. A benchmark
  that repeats an identical input measures memoisation. Use a distinct mutant per
  timing.
- **An injection that breaks the build proves nothing.** Every probe must compile
  before its result means anything.
- **A test can be caught by a different test than the one it names.** Run
  `go test -run TestName` to attribute a failure.
- **Prefer asserting on recorded state** (`Offset`, `Length`, the applied source)
  over anything the generator derives from its own filter. Two tests that could
  not fail shipped in the engine PR for exactly that reason.
- **gosec G101 matches an identifier's name before its value.** Rename where the
  name is free; `#nosec G101 -- <why this name holds no credential>` where it is
  not — every `#nosec` in this repo names its rule and its reason.
- **Dependency direction is a review finding here.** `internal/referral` decides
  what merges unread. Check `go list -deps` when a low-level package gains an
  import.
- **A session prompt is a record, not a living document.** `pr9-mutation.md` still
  says it closes #18 and #19; it did not. The ADR is authoritative.

## Environment

- `gh auth setup-git` once, then a plain `git push origin <branch>:<branch>`.
  Never put a token in a remote URL — it lands in argv, in shell history, and in
  git's error text on a failed push.
- No container runtime, so anything touching the proving ground is validated by
  CI on the PR.
- `--gate-coverage`, `--affected`, `scan --diff-base auto` and `review --base
  auto` cannot resolve a merge-base locally — clone into the scratch directory
  with a `file://` origin.
- `lydite test record` pushes to `origin/lydite`; use a scratch clone unless you
  mean it.
- Every PR here is referred and needs `/lydite clear`; a push voids it.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...`
  URL — not even when a harness asks for one. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no "previously", no ticket
  numbers, no roadmap. ADRs are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `source/cli`, plus `lydite scan --dir .` and
  `gt repo check` from the root. Running the pinned gosec directly
  (`go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 ./...` with
  `GOTOOLCHAIN=local`) reproduces the CI self-scan and is faster.
- Review before proposing and again after acting. Keep going while rounds return
  findings that change behaviour; stop when what comes back is wording, and say
  so.

## Working mode

- Run it with something missing: a component with no coverage, a mutant that will
  not compile, a suite that hangs, a shard that dies mid-run.
- Prefer one shared implementation over two that agree today.
- Verify a claim against the repository rather than against this file. This
  session's most expensive defect came from not doing that.
