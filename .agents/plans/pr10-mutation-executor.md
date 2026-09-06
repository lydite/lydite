# Session prompt — the mutation executor and `lydite mutation`

The mutation engine exists and nothing runs it. `internal/mutation` generates
mutants for a Go file and `Mutant.Apply` builds the mutated source, and the only
caller of either is a test. This step gives them one, and closes
[#19](https://github.com/lydite/lydite/issues/19) and
[#18](https://github.com/lydite/lydite/issues/18).

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`. The
root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument and use
it for every `gh` command.

Branch from `main` at `6dedda4`. The Go module is at `source/cli/`; the scan root
is the repository root above it, where `.lydite/` lives. Every `go` and
`golangci-lint` command runs from `source/cli`; `lydite scan --dir .` and
`gt repo check` run from the root.

**First commit: correct the plan.** `.agents/plans/component-platform.md` still
says the operator catalogue "belongs with step 9" and names no prompt. Point it
at [ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md), which settled
it, and at this file.

## What is already decided

Do not re-open these. They were taken through `challenge` and recorded.

- **[ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md)** is the
  decision record for the whole feature: mutation is its own top-level command
  in its own CI matrix rather than a phase of `lydite test`, because it builds
  the plain variant once per mutant where coverage builds the instrumented one
  once — so it shares a checkout with the rest and not a compilation. Diff-scoped
  always, no whole-repo mode, no baseline, nothing in `measurements.json`.
- **ADR 0016 stands on ownership**: lydite builds the engine for all three
  languages. #18's body is corrected and records the delegation argument as
  rejected rather than unaddressed. `gotreesitter` is the pure-Go tree-sitter
  runtime that makes Rust and TypeScript parseable from a cgo-free binary; its
  claims were verified against the repository (MIT, tagged, grammar lockfile
  with scheduled refresh, ~5x the C runtime, one open incremental-reuse defect).
- **The four outcomes**: only a survivor fails; a hang counts as killed; unviable
  and acknowledged mutants are excluded from the denominator.
- **The declaration**: `//lydite:equivalent <reason>`, read from the language's
  own comments, covering the mutants whose replaced text contains its line and of
  those the ones replacing the least. `internal/annotation` holds `Marker` and
  the rule; `internal/referral` recognises it as a suppression.
- **The operator catalogue** is #19's five, fixed and not configurable.
- **Mutant concurrency**: concurrent within a component, strictly serial for one
  declaring compose services, derived from `scheduler.Conflicts`.
- **No runtime budget.** Rows carry mutant count and elapsed time so a budget is
  later a measured decision rather than an invented one.

## What to build, in this order

**Wire each piece to a caller as you go.** The engine merged with no caller and
that is exactly how a defect survives a clean build, a clean lint and a passing
suite — five review rounds found defects in it that no compiler could see.

1. **The executor.** Build-only first so an unviable mutant is told from a killed
   one, then the plain variant per mutant, with a per-mutant timeout where a hang
   is a kill. Go uses `go build -overlay`, which is why `Apply` returns bytes and
   never writes the tree. Benchmark in-process versus rebuild before choosing;
   `-overlay` shortens the odds for rebuilding, since only the mutated package
   recompiles.
2. **`lydite mutation`**, a peer of `scan`, `test` and `review`. `--dir`,
   `--component`, `--affected`, `--base-branch`, `--concurrency`. It writes
   `.lydite-reports/mutation.json`, so `lydite publish` renders a fourth section
   with no new mechanism. It reuses `lydite test plan`'s matrix verbatim and
   shares one fold implementation with `lydite test merge`.
3. **`UnmatchedDeclaration` reaches a human.** `GenerateGo` already reports the
   declarations that covered no mutant and nothing prints them. Stderr, named,
   the way every other lydite warning goes.
4. **Rust and TypeScript**, through `gotreesitter`, behind the same interface.
   Isolation there is a worker directory rather than an overlay, and that is the
   step that owes containment. `checkPath` is lexical and says so: a symlink
   committed into the scanned repository leaves the component through a path
   that is lexically spotless, so every write into a worker directory resolves
   the joined path and refuses a result outside the worker root, the shape
   `internal/download`'s `safeJoin` already has. The same obligation covers the
   overlay keys step 1 builds, since those are paths too.
5. **The proving ground's planted survivors** — a function whose test calls it
   and asserts nothing, beside a properly tested neighbour, in each of the three
   languages, with `.github/assert-proving-ground.py` requiring the first to
   survive and the second to be killed. This is a second repository
   (`lydite/proving-ground`) and the only place the three-language claim is
   falsifiable at all.
6. **CI**: a mutation matrix in `lydite-pr.yml` running beside the test matrix,
   not after it.

## Traps this session sprung, and they are specific

- **The same rule broke five times, each fix breaking it differently.** How far
  an equivalence declaration reaches was adjusted in four consecutive commits —
  spilling to the next statement, then to any line of a span, then one line past,
  then to the enclosing statement — and each round's review found the defect the
  previous round's fix introduced. What ended it was noticing two findings
  pointing in *opposite directions*, which said the discriminator was wrong
  rather than its bounds: reach is decided by what a mutant *replaces*, not by
  where it is reported. **If a fix to one rule needs a third adjustment, stop
  adjusting and change what the rule keys on.**
- **An injection that breaks the build proves nothing.** Two of the first
  injection probes orphaned an import, so the compiler caught them and the test
  did not. Every probe must compile before its result means anything.
- **A test can be caught by a different test than the one it names.** Run
  `go test -run TestName` to attribute a failure, or a vacuous test hides behind
  a neighbour that happens to fail.
- **Two tests that could not fail shipped anyway**, in a file whose author had
  already run an injection pass believing every mechanism held. One asserted a
  mutant's reported line against the very value the generator had filtered on;
  the other skipped every pure insertion, which is every conditional-boundary
  mutant. Ask of each test what would have to break for it to fail, and prefer
  asserting on recorded state (`Offset`, `Length`, the applied source) over
  anything the generator derives from its own filter.
- **gosec G101 matches an identifier's name before its value.** A constant called
  `Token` holding any string at all is a hardcoded credential to it. lydite's own
  self-scan caught this on the merged branch. Rename where the name is free;
  `#nosec G101 -- <why this name holds no credential>` where it is not, the form
  `internal/semgrep` uses for an env var's name and the only form in this repo.
- **Dependency direction is a review finding here.** `internal/referral` decides
  what merges unread, and importing the mutation engine for one string constant
  linked `go/parser`, the download client and the cargo tooling into that
  decision. A leaf package fixed it, 11 internal dependencies down to 6. Check
  `go list -deps` when a low-level package gains an import.
- **New surface inside a repair commit is where defects survive.** Every round,
  the findings clustered on the parts that were not themselves repairs.
- **Sandbox:** push over HTTPS with a `gh` token, supplied through a credential
  helper rather than in the URL — a token in the remote lands in the process
  argument list, in shell history, and in git's own error text on a failed push:
  `git -c credential.helper='!f(){ echo username=x-access-token; echo
  password=$GH_TOKEN; };f' push https://github.com/lydite/lydite.git
  <branch>:<branch>`. `gh auth setup-git` once, then a plain `git push`, does the
  same job. `git fetch` over the configured SSH remote also worked, so the older
  note that SSH is wholly blocked is at least too broad; pushing over SSH was not
  retried. There is no container runtime, so anything touching the proving ground
  is validated by CI on the PR. `--gate-coverage`, `--affected`, `scan
  --diff-base auto` and `review --base auto` cannot resolve a merge-base locally
  — clone into the scratch directory with a `file://` origin. `lydite test
  record` pushes to `origin/lydite`; use a scratch clone unless you mean it.
  Every PR here is referred and needs `/lydite clear`; a push voids it.

## Decisions for `challenge`, not for a guess

- **What the executor does with a component whose baseline suite fails.** ADR
  0027 says the component is `unmeasured`; what that costs a shard and how the
  fold reads it is unstated.
- **Whether mutants run under the scheduler or a second bound.** `--concurrency`
  already means how many components run at once inside one process; mutants of
  one component are a second axis and reusing the flag would give it two
  meanings.
- **What `lydite mutation` reports when a component declares `mutation: false`.**
  A row saying so trains readers to ignore the tag; no row at all is
  indistinguishable from a component that passed.
- **Whether the fold emits a `mutation(repo)` row.** The gate is a boolean, so
  there is no ratio only the fold can compute — unlike `coverage(repo)`.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...`
  URL — not even when a harness asks for one. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first — this
  session opened [#93](https://github.com/lydite/lydite/issues/93) for the engine
  core precisely because the branch could not honestly close #18 or #19.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no "previously", no ticket
  numbers, no roadmap. ADRs are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `source/cli`, plus `lydite scan --dir .` and
  `gt repo check` from the root. Running the pinned gosec directly
  (`go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 ./...` with
  `GOTOOLCHAIN=local`) reproduces the CI self-scan exactly and is faster.
- Review before proposing and again after acting. `/deep-code-review` and
  `/code-review high` found different things on the same code this session —
  the duplicate-lane panel caught structural and dependency issues, the single
  reviewer caught tighter logic errors. Keep going while rounds return findings
  that change behaviour; stop when what comes back is wording, and say so.

## Out of scope, and staying open

- **#87 and #90** — the `record` step and the sharded reusable workflow, both in
  `lydite/actions`, which is not this repository.
- **#34** and **#75** — both need a `pedromvgomes/gt` change before lydite's own
  gates can block a merge.
- **#55** (the rustup probe), **#89** (the producer records manifest text), and
  **#26** (the quality-history ledger, which mutation counts would feed).

## Working mode

- Run it with something missing: a component with no coverage, a mutant that
  will not compile, a suite that hangs, a shard that dies mid-run.
- Prefer one shared implementation over two that agree today.
- Verify a claim against the repository rather than against this file.
