# The component platform

Rolling plan for the work specified in
[ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md). The ADR is
the specification and wins on any disagreement; this file tracks how it is being
built and what is left. The `pr*.md` files beside it are session prompts.

## Order

| # | Issue | Deliverable | Prompt | State |
|---|---|---|---|---|
| 0 | #38 | Proving ground: polyglot repo in the `lydite` org | `pr0-proving-ground.md` | not started |
| 1 | — | `internal/component`, `internal/runner` (go/rust/ts), `lydite test`, `.lydite/` layout | `pr1-component-model.md` | not started |
| 2 | — | Orphan-file gate | — | not started |
| 3 | — | `internal/compose`: runtime probe, ports, up/wait/down | — | not started |
| 4 | — | Scheduler: resource-constrained queue, port locks, local driver | — | not started |
| 5 | — | Affected selection; full run on the default branch | — | not started |
| 6 | #36 | Coverage onto components; `coverage.source` removed | — | not started |
| 7 | — | Scan onto components; `internal/detect` deleted | — | not started |
| 8 | — | `lydite matrix`, reusable workflow, dogfood in `ci-test.yml` | — | not started |
| 9 | #18 #19 | Mutation, on top of all of the above | — | not started |

Steps 0 and 1 run in parallel, in separate worktrees. Step 1 cannot merge until
step 0 exists, because two of its three runners have nothing to run against
otherwise.

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
- ADR 0010 needs a line reconciling a third repository with its "Two repos, not
  three" section: a proving ground ships nothing and is never released.

## Known traps

- **#36 before step 6.** Adding `-coverpkg` and moving coverage onto components
  both change the reported number. Done separately they cost two baseline resets
  and two confusing releases.
- **`go test -overlay` is Go-only.** `cargo` has no equivalent, so mutation
  isolation is a strategy behind one interface: overlay for Go, worker
  directories for Rust and TypeScript.
- **An unviable mutant is indistinguishable from a killed one by exit code.**
  Both exit 1. Build before testing, or the mutation score silently inflates.
- **The local scheduler is the path CI never runs.** Agent skills requiring a
  local run before opening a pull request are what exercise it.
