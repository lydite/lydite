# lydite runs the tests, and the component is the unit of work

lydite reads coverage that someone else's job produced, and runs its checks
sequentially over units it discovers by walking for manifests. Mutation testing
breaks both halves of that: it must build and execute a suite to learn anything,
and it is expensive enough that running it sequentially over a repository is not
a gate anyone would keep switched on.

So a repository declares its **components** in `.lydite/components.yml`, and
lydite builds, tests, measures and gates each one — in parallel, as CI matrix
jobs or as local processes, from one declaration that both sides read.

## Orchestrator, not runner

lydite does not know how to run anyone's tests, and must not learn. It invokes
`go test`, `cargo nextest`, `vitest`; it owns scheduling, isolation, service
lifecycle, report collection and gating *around* those commands.

The distinction is the whole risk in this decision. Every repository's test
invocation is bespoke at the edges — tagged builds, ignored tests that need a
real database, workspace filters, custom profiles — and a design that tries to
absorb that ends up as a worse version of each language's own tooling. The
component declares what to run. lydite decides where, when, how many at once,
and what the result means.

## A component is a build unit

A component is the unit that language's build tool treats as a whole: a Cargo
workspace, a Go module, a JavaScript workspace. Not a deployable, and the two
come apart constantly.

`wardnet-cloud`'s `cloud-services` workspace holds eleven crates across three
services and is tested by one command. Declared as three components — one per
service — it would compile three times and provision three databases, and the
local scheduler would serialise all three on the same port. Declared as eleven
it is worse still. It is one component. The same holds for a JavaScript
workspace whose packages share one `node_modules` and one install.

Where the build tool genuinely separates things, so do components: `wardnet`'s
`sdk/wardnet-go` and `wctl` are distinct Go modules and distinct components.

This is what makes one matrix job per component the right mapping, with no
batching. Job overhead looked like a reason to group small components until the
granularity was right; at the build unit, there are few enough components that
per-job setup is noise. It is also the rule most likely to be got wrong by
someone declaring the architecture they have in their head, which is why it is
stated first.

## Declared, not discovered

Components are written down. Nothing is inferred from the tree.

lydite's other configuration goes the other way — `.lydite/config.yml` exists to
opt *out* of a zero-config default that scans everything detected — so this is a
deliberate departure. The reason is that the declaration is the reviewable
statement of what gets tested, and its history is the record of every change to
that. Detection cannot produce that artefact, and detection also cannot tell a
buildable unit from a manifest that exists for another purpose: lydite's own
`internal/golang/go-pin/go.mod`, `internal/typescript/biome-pin/package.json`
and `internal/rust/cargo-audit-pin/Cargo.toml` are real manifests and are not
components.

A declared list fails open — nothing breaks when it goes stale, so new code is
simply never tested and the build stays green. The guard is not to resurrect
detection but to check for **orphan source files**: every source file must fall
under some component's directory or an explicit exclude. That needs no
per-language knowledge, and it catches a whole undeclared directory, including
one that has no manifest yet. It is a gate, cleared by declaring the component.

## Runners, not commands

A component names a runner — `go-test`, `cargo-nextest`, `cargo-llvm-cov-nextest`,
`vitest` — plus arguments. lydite needs the same suite three ways: plain for
mutants, instrumented for the coverage gate and for mutation's baseline, and
build-only to tell an unviable mutant from a killed one. Those three are a
property of the runner, not of the repository, so deriving them from one
declaration is what stops them disagreeing about which tests they run.

Instrumentation is not a flag that can be spliced into an arbitrary command. Go
appends `-coverprofile`; Rust replaces the runner outright with
`cargo llvm-cov`. A single command carrying a `{coverprofile}` placeholder
covers the first case and has nowhere to put the second.

A raw `command:` remains as an escape hatch. A component using it opts out of
derived variants and declares what it needs itself.

## Services are a compose file

A component's services are declared by pointing at a compose file, naming which
of its services to bring up, and how to wait for them.

lydite therefore owns no service schema. Images, ports, environment and
healthchecks are already compose's job, and a second description of them could
only agree redundantly or drift. It also cannot hard-code a runtime: the compose
implementation is a property of the machine, not of the repository — podman on a
laptop, docker on a runner — so lydite probes for one and names which it chose.

The runner's own native service block is not used, even in CI where it is
better at lifecycle and health checks. It exists only in CI, and a service
declaration that works in exactly one of the two places is the thing this design
is trying to remove.

`wait: healthy` requires the compose service to declare a healthcheck, and lydite
refuses when it does not, rather than degrading to "started". Tests racing a
database that is not yet listening are the flakiest thing a pipeline can contain,
and the failure is attributed to the test rather than to the wait.

`setup` and `teardown` commands cover what is not a container — migrations,
fixtures, cleanup. Teardown runs even when the run fails; leaked containers
poison the next local run. Nothing keeps a stack alive between runs:
[ADR 0017](0017-shards-the-scheduler-and-the-planner.md) records why the
`--keep-services` this section originally described does not survive the port
lock, and why `down --volumes` makes it a determinism question rather than only
a scheduling one.

## Scheduling is resource-constrained

Components run in parallel, except that a component holds a lock on each host
port its compose services publish. Two components publishing the same port run
one after the other.

The lock is keyed on ports rather than on service names because the conflict is
physical. Two components each calling a service `db`, on different ports, do not
conflict and must not be serialised; two calling them `db` and `postgres` on the
same port do, and a name-keyed lock would miss it and fail mid-run on a bind
error. lydite already reads the compose file to know what to start, so reading
`ports:` costs nothing.

Sharing one running service between concurrent components is rejected. Two
suites against one database truncate each other's tables, and the failure is
non-deterministic and reads as a bad test. Making it safe means injecting a
distinct database per component, which puts lydite back in charge of connection
details that the compose delegation exists to keep out of it.

The scheduler runs inside a **shard** — see
[ADR 0017](0017-shards-the-scheduler-and-the-planner.md). A shard holding
several components is produced deliberately rather than merely tolerated,
because a scheduler that never runs two components at once passes every test
about port locks without ever having taken one.

## Affected selection, with the default branch as the backstop

A pull request runs a component when the diff touches its directory, touches one
of its `watch` paths, touches a component it `depends_on` transitively, or
touches a global invalidator — a workspace manifest, a lockfile, a toolchain
file, or `.lydite/` itself.

`watch` exists because real repositories have it: `wardnet`'s daemon is
invalidated by `Makefile` and `VERSION`, and its Go component by
`docs/openapi.json`. Those are files under no component that affect one
specifically.

Dependency edges are declared for the same reason components are, and because
they are not always derivable at all. `wardnet` regenerates a Go client from
`docs/openapi.json`, and no tool sees that edge.

A forgotten edge means a dependent is not run on the change that broke it, and
the orphan check does not catch it, because both components exist. **Pushes to
the default branch therefore run every component.** A missing edge surfaces at
merge instead of never, which turns affected-selection from a correctness
mechanism into an optimisation with a bounded failure. Default-branch runs
already have to be complete, since they record the coverage baselines.

## One job per shard, containing all of its checks

A matrix job is a set of components, not a check.
[ADR 0017](0017-shards-the-scheduler-and-the-planner.md) names that set a
**shard** and explains why the unit is not a single component: under one
component per job nothing in CI ever contends for a port, and the scheduler's
lock below is exercised nowhere automated. Scanning, testing, coverage and
mutation for that component share a job because they share a compilation:
`wardnet-cloud` runs `cargo fmt`, `cargo clippy` and its coverage run together
for exactly that reason, and splitting them across jobs compiles the workspace
twice.

The matrix is computed by lydite and consumed by the repository's own
`ci-test.yml`, which calls a reusable workflow. gt owns which stages exist;
the stages themselves belong to the repository and gt never modifies them, so
this needs no change to gt's governance. The one ceiling that remains is
permissions: a called workflow may only narrow what
`ci-orchestration.yml` grants, so a component job cannot obtain
`statuses: write` without a gt change.

## Consequences

- `coverage.source` is removed rather than deprecated. Once lydite runs the
  suite, "who produces coverage" has one answer, and a setting whose only
  remaining value describes a pipeline lydite no longer has is a way to
  configure a contradiction.
- Configuration moves under one directory: `.lydite/config.yml`,
  `.lydite/components.yml`, `.lydite/exemptions.yml`. A dotfile beside a
  dot-directory that both configure the same tool is an arrangement nobody can
  predict the shape of.
- `internal/detect` loses its purpose. Tests, coverage, scanning and mutation
  all learn where to run from the component. Orphan detection replaces it and
  needs none of its per-language knowledge.
- JUnit is collected per component, because the quality-history ledger records
  test counts and both reference pipelines already emit it.
- Codecov is not migrated. lydite owns every number it reports (ADR 0009), so
  the upload, its token, and the bot-authored-pull-request handling that token
  requires all go rather than moving.
- Rollout is dogfooding first, then the simplest consumers, with `wardnet` and
  `wardnet-cloud` last. Both have working pipelines that this replaces
  wholesale, and they are the two repositories where being wrong costs the most.

## lydite owns mutation in every language

Mutation is built rather than delegated, for Go, Rust and TypeScript alike.

The objection to this was that mutating a language means parsing it, and lydite
ships as one statically linked binary with cgo disabled — so Rust and
TypeScript would need a helper binary or a compiler embedded. That objection is
wrong. `gotreesitter` is a pure-Go tree-sitter runtime, MIT, tagged, whose
grammar tables are generated from the upstream tree-sitter grammars pinned by
commit in a lockfile and refreshed on a schedule. Parsing Rust and TypeScript
and splicing a mutant at node offsets works from a cgo-free binary.

What delegation would have cost is consistency, and it is not the consistency of
a score: the criterion is zero unacknowledged survivors, a boolean, which
survives differing operator sets. It is that each tool brings its own diff
scoping, its own skip annotation, its own idea of what counts as a survivor, and
lydite would spend its effort normalising three foreign models instead of
holding one. Owning the engine means one operator taxonomy, one acknowledgement
model feeding `internal/referral`'s disqualifiers, and one diff scope shared with
coverage and referral.

Two costs are accepted rather than avoided. A syntax tree carries no types, so
mutations needing a plausible replacement value are weaker than a type-aware
tool would manage. And only Go has `go build -overlay`, which lets a mutant be
compiled without ever touching the tree; Rust and TypeScript need a worker
directory instead. Isolation is therefore a strategy behind one interface, and
the pipeline above it is identical in all three languages.

## What this does not settle

Mutation's operator catalogue — how far past binary operators and boolean
literals to go, given each operator multiplies runtime. That belongs with the
mutation slice, which sits on top of this one. What is settled is that mutation
runs per component, inside that component's job, against the same declaration
everything else uses.
