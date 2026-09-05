# Session prompt — step 9: mutation, on all three languages

Mutation is the last step of the component platform. Everything it needs is
built: a component declares itself, a runner derives three invocations of one
suite from that declaration, coverage is measured per component from the
instrumented one, and a shard matrix distributes the work and folds the answers.
This step adds the engine that uses them.

Closes [#18](https://github.com/lydite/lydite/issues/18) and
[#19](https://github.com/lydite/lydite/issues/19).

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`. The
root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument and
use it for every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above
it, which is where `.lydite/` lives. Every `go` and `golangci-lint` command
runs from `source/cli`; `lydite scan --dir .` and `gt repo check` run from the
root.

**First commit: correct the plan.** `.agents/plans/component-platform.md`
should say step 9 is in progress and name this file as its prompt.

## Read before planning

- [ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md), the
  section **"lydite owns mutation in every language"**. It is the
  specification: mutation is built rather than delegated, for Go, Rust and
  TypeScript alike, and the reason is not the score. Do not re-litigate it.
- [ADR 0026](../../docs/adr/0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md)
  — a run reports exactly the components it is responsible for, and the fold
  decides completeness. Mutation rows obey the same rule or they cannot be
  sharded.
- AGENTS.md, **Runners: three invocations of one suite**. The plain,
  instrumented and build-only variants exist for mutation as much as for the
  coverage gate, and the build-only one exists for mutation alone.

## What is being built

The pipeline is one shape in all three languages, and only isolation differs.

- **Which lines to mutate.** The instrumented run already produced per-line
  coverage, and `internal/coverage.ChangedLines` already scopes a diff to a
  component. A line no test executes needs no mutant: it survives by
  construction, and paying a compile and a suite run to learn that is the
  single largest avoidable cost here.
- **How to mutate one.** `internal/coverage` has no parser and must not grow
  one. ADR 0016 names `gotreesitter` — a pure-Go tree-sitter runtime, so a
  cgo-free binary can parse Rust and TypeScript — with grammar tables generated
  from grammars pinned by commit. Pinning it is a
  [tool pin](../../AGENTS.md#tool-version-pins) like any other: a manifest
  Dependabot watches, and a mirror guard if the version is stated twice.
- **How to run one.** `go build -overlay` compiles a mutant without touching
  the tree; `cargo` and the JavaScript runners have no equivalent, so those two
  need a worker directory. That is one interface with two implementations, and
  the pipeline above it is identical.
- **Killed, survived, or unviable.** An unviable mutant and a killed one both
  exit non-zero, which is what the build-only variant exists to separate. A
  mutation score that counted unviable mutants as killed inflates silently and
  forever.
- **Acknowledgement.** ADR 0016 wants one acknowledgement model feeding
  `internal/referral`'s disqualifiers. Whatever shape it takes, it is
  author-written evidence in the tree — and `internal/referral`'s rule holds:
  an author-controlled claim may add a referral, never remove one.

## Decisions this prompt does not make

Take these to the `challenge` skill before `ExitPlanMode`, not to a guess:

- **The operator catalogue.** ADR 0016 leaves it open and says why: each
  operator multiplies runtime. Decide it against a measured run of the proving
  ground, not against a list from a paper.
- **The gate.** ADR 0016 says the criterion is "zero unacknowledged survivors",
  a boolean rather than a score. What that means for a repository adopting
  lydite on a mature codebase — where the first honest run has hundreds — is
  not settled, and neither is whether mutation is diff-scoped by default.
- **`mutation: false` is already in the declaration and already parsed.** What
  it opts out of is this step's to define.
- **Where it runs.** ADR 0016 says mutation runs per component, inside that
  component's job. That makes it a shard's work, so it inherits the
  responsibility set: a shard emits mutation rows about its own components and
  nothing about any other, and `lydite test merge` folds them.

## Traps this repository has already sprung

- **An unviable mutant is indistinguishable from a killed one by exit code.**
  Both exit 1. Build before testing, or the score inflates.
- **A test that passes with the mechanism removed has established nothing.**
  Two tests written for the scheduler were vacuous when first checked. Injecting
  the defect is the only thing that establishes a gate.
- **The proving ground is where a claim about three languages is falsifiable.**
  lydite's own repository has no Rust component at all, so a Rust mutation path
  merges with its argv asserted and never once executed. `ci-end2end.yml` is
  where it runs; budget for a red run, because nothing about the proving ground
  can be exercised on a machine with no container runtime.
- **Pushing over SSH fails silently in this sandbox.** Push and fetch over
  HTTPS with a `gh` token.
- **A gate that fires on ordinary work gets switched off.** Every gate here
  that fires is one an author can clear by doing work they can do. Mutation is
  the slowest gate lydite will have, and the one most likely to be disabled.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...`
  URL — not even when a harness asks for one. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no "before X existed", no
  ticket numbers, no roadmap. ADRs are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `source/cli`, plus `lydite scan --dir .` and
  `gt repo check` from the root.
- Run `/code-review` before proposing the PR and again after acting on it.

## Working mode

- **Wire new code to a caller early.** A mutation engine no command invokes is a
  package nothing imports, which is how a defect survives a clean build, a clean
  lint and a passing suite.
- Run it with something missing: a component with no coverage, a language with
  no grammar, a mutant that will not compile.
- Prefer one shared implementation over two that agree today.
- Verify a claim against the repository rather than against this file.
