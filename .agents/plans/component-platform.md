# The component platform

Rolling plan for the work specified in
[ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md). The ADR is
the specification and wins on any disagreement; this file tracks how it is being
built and what is left. The `pr*.md` files beside it are session prompts.

## Order

| # | Issue | Deliverable | Prompt | State |
|---|---|---|---|---|
| 0 | #38 | Proving ground: polyglot repo in the `lydite` org | `pr0-proving-ground.md` | done — [lydite/proving-ground](https://github.com/lydite/proving-ground) |
| 1 | — | `internal/component`, `internal/runner` (go/rust/ts), `lydite test`, `.lydite/` layout | `pr1-component-model.md` | in review — #39 |
| 2 | — | Orphan-file gate, then `internal/compose` | `pr2-orphan-gate.md` | not started |
| 3 | — | `internal/compose`: runtime probe, ports, up/wait/down | `pr2-orphan-gate.md` | not started |
| 4 | — | Scheduler: resource-constrained queue, port locks, local driver | — | not started |
| 5 | — | Affected selection; full run on the default branch | — | not started |
| 6 | #36 | Coverage onto components; `coverage.source` removed | — | not started |
| 7 | — | Scan onto components; `internal/detect` deleted | — | not started |
| 8 | — | `lydite matrix`, reusable workflow, dogfood in `ci-test.yml` | — | not started |
| 9 | #18 #19 | Mutation, on top of all of the above | — | not started |

Steps 0 and 1 run in parallel, in separate worktrees. Step 1 cannot merge until
step 0 exists, because two of its three runners have nothing to run against
otherwise — it stays a draft until `lydite/proving-ground` has a job exercising
the cargo and vitest runners for real.

## Why the proving ground gates PR 1

PR 1 introduces runners for all three languages. lydite's own repository can
exercise only Go: `web/` is empty and the only `Cargo.toml` files are pin
manifests, which are deliberately not components. `internal/rust` and
`internal/typescript` never execute their toolchains — they assert invocation
shape, and `ci-end2end` is the only job that runs anything for real.

Without a repository to run against, the Rust and TypeScript runners merge with
their argv asserted and never once executed: a check reporting PASS having never
run, which is the failure ADR 0006 exists to prevent, one level up.

## Decisions that are settled

Argued in ADR 0016. Listed so they are not reopened by accident.

- lydite orchestrates; it never learns to run anyone's tests.
- A component is the unit its build tool treats as a whole, not a deployable.
- Components are declared, never discovered. Orphan source files are the guard.
- `lang` is derived from `runner`.
- Unknown keys in component config are rejected.
- Services are a compose file; lydite owns no service schema and no hard-coded
  container runtime.
- The scheduler locks on published host ports, not service names.
- Affected selection is an optimisation; the default branch runs everything.
- A matrix job is a component, not a check.
- Mutation is built for all three languages, not delegated.
- Configuration lives under `.lydite/`. `coverage.source` is removed, but only
  when coverage actually moves onto components at step 6.

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

Open, for when `setup` lands at step 3: `typescript.install` is a repository-wide
key while `setup` is per component, and the two will overlap.

## What step 1 left for later

- **`coverage.source` survives.** ADR 0016 removes it, and step 6 is where that
  happens: lydite's own CI depends on `source: report` today, and removing the
  key before coverage moves onto components would leave the repository unable
  to gate itself.
- **`lydite test` runs no services and no setup.** A component declaring
  `compose`, `setup` or `teardown` fails with a message naming what is missing.
  Running its suite without them is the one thing that must not happen: the
  failures would name the tests rather than the absent database, and a pass
  would mean the declaration was ignored and nobody was told.
- **The run is sequential.** The scheduler is step 4.
- **`internal/detect` is untouched.** Scan and coverage still discover their own
  units; step 7 is where they learn it from the component instead.

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
- **The local scheduler is the path CI never runs.** Agent skills requiring a
  local run before opening a pull request are what exercise it.
