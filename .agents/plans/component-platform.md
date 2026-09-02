# The component platform

Rolling plan for the work specified in
[ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md). The ADR is
the specification and wins on any disagreement; this file tracks how it is being
built and what is left. The `pr*.md` files beside it are session prompts.

## Order

| # | Issue | Deliverable | Prompt | State |
|---|---|---|---|---|
| 0 | #38 | Proving ground: polyglot repo in the `lydite` org | `pr0-proving-ground.md` | done — [lydite/proving-ground](https://github.com/lydite/proving-ground) |
| 1 | — | `internal/component`, `internal/runner` (go/rust/ts), `lydite test`, `.lydite/` layout | `pr1-component-model.md` | done — #39 |
| 2 | — | Orphan-file gate | `pr2-orphan-gate.md` | done |
| 3 | — | `internal/compose`: runtime probe, ports, up/wait/down | `pr1-component-model.md` | done — folded into #39 |
| 4 | — | Scheduler: port locks and the in-shard concurrency bound | `pr4-scheduler.md` | done — #44 |
| 5 | — | Affected selection; full run on the default branch | `pr5-affected-selection.md` | done — #46 |
| 6 | #36 | Coverage onto components; `coverage.source` removed | `pr6-coverage-on-components.md` | in progress — challenged, see [ADR 0019](../../docs/adr/0019-coverage-per-component-gated-by-lydite-test.md) |
| 7 | — | Scan onto components; `internal/detect` deleted | — | not started |
| 8 | — | `lydite test plan` and `lydite test merge`, reusable workflow, dogfood in `ci-test.yml` | — | not started |
| 9 | #18 #19 | Mutation, on top of all of the above | — | not started |

Steps 0 and 1 ran in parallel, in separate worktrees. Step 1 could not merge
until step 0 existed, because two of its three runners had nothing to run
against otherwise.

**Step 3 is folded into step 1.** Both of the proving ground's
service-declaring components are the ones the cargo runner would exercise, so
"the cargo runner has been executed once" and "lydite can start a component's
services" turned out to be the same requirement.

## Why the proving ground gates PR 1

PR 1 introduces runners for all three languages. lydite's own repository can
exercise only Go: `web/` is empty and the only `Cargo.toml` files are pin
manifests, which are deliberately not components. `internal/rust` and
`internal/typescript` never execute their toolchains — they assert invocation
shape, and `ci-end2end` is the only job that runs anything for real.

Without a repository to run against, the Rust and TypeScript runners merge with
their argv asserted and never once executed: a check reporting PASS having never
run, which is the failure ADR 0006 exists to prevent, one level up.

`ci-end2end`'s `proving ground` job is what closes it, running `lydite test`
against every component of a bare checkout. Nothing in that job prepares the
tree — no dependency install, no services started — because a runner that only
works after the workflow has done that is one the job would never catch failing
for a consumer.

## Decisions that are settled

Argued in ADR 0016. Listed so they are not reopened by accident.

- lydite orchestrates; it never learns to run anyone's tests.
- A component is the unit its build tool treats as a whole, not a deployable.
- Components are declared, never discovered. Orphan source files are the guard,
  and the guard is a path question — no manifest read, no source parsed.
- `lang` is derived from `runner`.
- Unknown keys in component config are rejected.
- Services are a compose file; lydite owns no service schema and no hard-coded
  container runtime.
- The scheduler locks on published host ports, not service names.
- Affected selection is an optimisation; the default branch runs everything.
- Selection widens on ignorance: a changed path matching nothing selects every
  component, which makes the invalidator set a performance concern rather than a
  safety one. See [ADR 0018](../../docs/adr/0018-selection-widens-on-ignorance.md).
- A matrix job is a **shard** — a set of components lydite runs in one process —
  and not a check. The scheduler runs inside a shard, so a job holding several
  components is the case that keeps the port lock exercised. See
  [ADR 0017](../../docs/adr/0017-shards-the-scheduler-and-the-planner.md).
- Mutation is built for all three languages, not delegated.
- Configuration lives under `.lydite/`. `coverage.source` is removed, but only
  when coverage actually moves onto components at step 6.
- Coverage is measured per component, from the runner's instrumented variant.
  `lydite test` measures and gates it and `lydite coverage` is removed; a
  baseline is per-component line counts keyed by tree. See
  [ADR 0019](../../docs/adr/0019-coverage-per-component-gated-by-lydite-test.md).
- A component rooted at `.` no longer suppresses affected selection's widening.
  `dir: .` says where a component is rooted, not that it tests every file.

## Still open

- Mutation's operator catalogue — how far past binary operators and boolean
  literals, given each operator multiplies runtime. Belongs with step 9.
- Two questions the schema raised on first contact with a repository, both for
  step 1 to settle. `cargo-llvm-cov-nextest` is listed as a runner name beside
  `cargo-nextest`, but instrumentation is a *derived variant* and Rust's
  instrumented variant already replaces the runner with `cargo llvm-cov` — so a
  component declaring it has an instrumented variant of an instrumented runner.
  And nothing names a JavaScript package manager or workspace filter, so whether
  `vitest` runs at the workspace root or per package can only be said in `args`.

## Toolchains are resolved per ecosystem, not per component

`internal/toolchain.Requirements` reads every manifest under the scan root and
returns **one requirement per language**. A component is a build unit that
states its own versions, so the component model makes per-component resolution
the natural shape — and `lydite test` provisions nothing at all today.

What that costs right now, measured against the code rather than assumed:

- **Go and Rust are fine.** Go's directive is a minimum and the toolchain is
  backward-compatible, so the highest directive across modules builds all of
  them. Rust already resolves per crate at *invocation* time, because rustup
  reads `rust-toolchain.toml` from the directory cargo runs in and
  `RUSTUP_TOOLCHAIN` is set only for a `.lydite/config.yml` override. The one
  rough edge is that only the highest channel is pre-materialised, so a crate
  on an older one has rustup install it in the middle of `cargo clippy` —
  which is what pre-materialising exists to avoid.
- **Node is the real gap.** One runtime is installed, at the highest
  `engines.node` found. Two workspaces needing Node 20 and Node 24 both get 24.
- **TypeScript's own version is not a toolchain and needs nothing.** The
  compiler is a devDependency of each workspace, Biome parses TypeScript with
  its own parser and depends on no compiler package, and both the test runner
  and `tsc --noEmit` resolve out of the component directory. A repository
  mixing TypeScript 5 and 7 across components already works.

Belongs with step 7, where scan learns its units from the component.

## The package manager is not a configuration key

The lockfile is the declaration. `package-lock.json`, `yarn.lock` and
`pnpm-lock.yaml` are committed facts, so a key naming the manager could only
restate one and then drift from it — the same argument that keeps toolchain
versions in the repository's own manifests.

The single case detection cannot answer is a root carrying two lockfiles, and
`typescript.install` already answers it, more completely than a manager name
could: it also expresses a Corepack-pinned flow and a monorepo install that no
single manager name implies. `internal/nodedeps` is where that rule lives, so
the coverage gate and the test run cannot disagree about it.

Open: `typescript.install` is a repository-wide key while a component's
`setup` is per component, and a JavaScript component can express its install
either way.

## What step 1 left for later

- **`coverage.source` survives.** ADR 0016 removes it, and step 6 is where that
  happens: lydite's own CI depends on `source: report` today, and removing the
  key before coverage moves onto components would leave the repository unable
  to gate itself.
- **Prebuilt first, source as the fallback.** cargo-nextest from source is seven
  minutes and the published archive is three, so `internal/cargotool` downloads
  the release and verifies its digest, falling back to `cargo install` for a
  platform with no release or a download that fails. `internal/download` holds
  the fetch/verify/unpack that `internal/toolchain` already had, because a
  second copy of a path-traversal guard is a second chance to get it wrong.
- **`cargo-llvm-cov` is still not installed.** `cargo-nextest` now is, pinned via
  `internal/cargotool`, but the instrumented Rust variant needs llvm-cov and
  nothing provisions it — which is the gap that silently caches an empty
  baseline under `coverage.source: run`. It belongs with step 6, where the
  instrumented variant is first actually run.
- **The run was sequential.** Step 4 consumes the published host ports
  `internal/compose` reads, through a `compose.LoadWith` pre-pass: a stack is
  the only thing that knows its ports, and the scheduler needs every
  component's before it decides what may run together.
- **`internal/detect` is untouched.** Scan and coverage still discover their own
  units; step 7 is where they learn it from the component instead.

## A component's output is captured, not streamed

`lydite test` writes every component's output to `.lydite-reports/<name>/test.log`
and puts the last 40 lines under a failing row. The scanners stream, because their
findings are the result; a suite's output is thousands of lines of passing tests,
and the first CI run against the proving ground proved the cost — `error: no such
command: nextest` sat fifty lines above the verdict that reported it, between two
components' container lifecycles.

`ui.Row.Log` carries the path into `--json`, so a consumer can link it. That is
what the PR comment at step 8 needs, and it is why the path travels as a field
rather than only inside prose a human reads.

## What the orphan gate settled

Three questions ADR 0016 left to the implementation, decided against the
proving ground rather than in the abstract:

- **A file counts only in a language lydite has a runner for.** The extension
  set sits beside the `Lang` constants in `internal/runner`, so a language
  gaining a runner and no extensions is one the gate is blind to. The
  calibration is the proving ground: six files there sit under no component
  and exactly one must fire. A README, a LICENSE, a Makefile, an OpenAPI
  document and a shell script are not code a component could claim.
- **Excludes live in `components.yml`.** An exclude states what goes untested,
  which is the one thing that file records, so its history stays the whole
  account of every widening rather than half of one.
- **The gate runs in `lydite test`, before selection and before any component
  runs.** Whether the declaration is complete does not depend on which
  components an invocation chose or on there being any — a repository
  declaring none is exactly the one whose every source file is orphaned.

The file list comes from git rather than a walk. A walk needs its own list of
directories to skip, which is a second copy of a judgement `.gitignore`
already holds, and the copy that drifts starts calling build output source.

`internal/pathmatch` now holds the anchored matcher `internal/referral` had.
Both decide something consequential off a path, and two matchers agree until
one learns about a pattern form the other has not.

## What the scheduler settled

Four questions ADR 0016 left open, decided in the challenge interview and
recorded in [ADR 0017](../../docs/adr/0017-shards-the-scheduler-and-the-planner.md).

- **A CI job holds a shard, not a component.** Under one component per job
  nothing in CI ever contends for a port, so the lock's only exercise would be a
  local run somebody has to remember to do. The planner (`lydite test plan`) and
  the merge command are step 8; the in-shard scheduler is step 4.
- **`--concurrency` defaults to 4, and is not derived from `NumCPU`.** Every
  runner lydite drives is already internally parallel, so one component already
  tries to use the whole machine. It is not 1 either — that leaves the port lock
  untaken everywhere it is not asked for explicitly.
- **`depends_on` is not a scheduling input.** lydite passes no artifact between
  components, so ordering on an invalidation edge costs parallelism to express a
  claim its author never made.
- **A failure never cancels the rest, and rows stay in declaration order.**
  GitHub Actions already has `fail-fast` at the layer that can cancel machines,
  and completion order puts a run's timing into a document meant to be diffable.

The lock covers a component's directory as well as its ports, by containment
rather than equality: `component.validate` enforces unique names and not unique
directories, and two components rooted at one tree install into and build in it
at once. `--keep-services` is gone — `down --volumes` exists so a suite that
truncates and reseeds starts from nothing, and keeping a stack alive made the
next run inherit the last one's data.

## What four review rounds kept finding

Every round found the same shape the orphan gate produced, and none of it was
caught by the build, the tests or the linter:

- An interrupted run exited 0, because unstarted rows are `unmeasured` and do
  not vote. Fixed in the exit code, and then found again in `--json`, which is
  the surface every consumer is told to read and the one the PR comment renders.
- The port lock read only the services named in `compose.up`, while compose
  starts their `depends_on` closure too — so a component naming an `app` held
  none of its database's ports.
- Probing for a container runtime before validating the declaration made
  `internal/compose`'s own tests pass or fail depending on whether the developer
  had docker installed.
- The CI assertion named the two colliding components by *directory* rather than
  by component name, so it matched nothing — and an assertion that matches
  nothing passes.

Two of the tests written for this step were themselves vacuous when first
checked: one passed with the port lock removed, and one prepended to `PATH`
rather than replacing it, so the runtime it meant to hide stayed resolvable.
**Injecting the defect is the only thing that established either.**

## Known traps

- **#36 before step 6.** Adding `-coverpkg` and moving coverage onto components
  both change the reported number. Done separately they cost two baseline resets
  and two confusing releases. `internal/runner`'s instrumented Go variant
  already carries `-coverpkg=./...`, and no reported number moves until step 6
  puts `internal/coverage` behind it — so the two land together, in one reset.
- **`go test -overlay` is Go-only.** `cargo` has no equivalent, so mutation
  isolation is a strategy behind one interface: overlay for Go, worker
  directories for Rust and TypeScript.
- **An unviable mutant is indistinguishable from a killed one by exit code.**
  Both exit 1. Build before testing, or the mutation score silently inflates.
- **A scheduler nobody contends is a scheduler nothing has tested.** Every test
  about port locks passes on a scheduler that never runs two components at once,
  because the lock is never taken. The proving ground's two components on 5432
  are the calibration, and the assertion is the observed concurrency and the
  observed serialisation — never the exit code.
- **The proving ground's correct verdict is not all-green.** Its
  `ci-end2end.yml` job asserts the orphan gate *fails* on `scripts/seed.ts`
  and stays silent on the excluded `generated/client.ts`. A change that makes
  that job green by removing either file has removed a branch of the gate from
  observation, not fixed anything.
