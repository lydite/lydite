# A matrix job is a shard, and the scheduler runs inside it

[ADR 0016](0016-components-and-lydite-run-tests.md) says a matrix job is a
component, and that components run in parallel except for a lock on each host
port their compose services publish. Those two statements pull in opposite
directions: if every CI job holds exactly one component, nothing in CI ever
contends for a port, and the lock's only exercise is a local run somebody has
to remember to do.

So a CI job holds a **shard** — a set of components lydite runs in one process
— and the scheduler runs inside a shard. Locally there is one shard holding
every selected component. In CI there is one shard per matrix job, and lydite
computes the grouping. `--concurrency max` puts one component in each, which is
what ADR 0016 described; anything lower groups them.

This amends ADR 0016's "one job per component" only in its unit. Everything
that section argued for is unchanged: scanning, testing, coverage and mutation
for a component still share a job because they share a compilation, and a job
is still not a check.

## Why the unit changed

A shard holding several components is the case that keeps the scheduler
honest. `lydite/proving-ground` declares two components that publish host port
5432 under services named `db` and `postgres` — differently named on purpose,
so a lock keyed on service names passes while being wrong. Under one component
per job those two never meet, the lock is never taken, and every test of it
passes without the constraint having been reached.

Under shards they meet in CI on every run of `ci-end2end.yml`'s proving ground
job, which is also the only job that executes the cargo and vitest runners at
all. That is the difference between a scheduler with an automated end-to-end
assertion and one with none.

## What the pieces are

- `lydite test --concurrency N` runs a shard: N components at once, serialising
  any pair that publishes a host port in common.
- `lydite test plan --concurrency N` is a pure function — read the declaration,
  read each compose file's ports, emit the grouping. No process, no state, no
  network. A CI workflow turns its output into a matrix.
- Each matrix job selects its components with the existing repeatable
  `--component` flag, carried in the matrix from the plan. The shard id is a
  label on the job and the artifact, never an input: passing both an id and a
  list would state one fact twice, and nothing would validate that the two
  agreed.
- `lydite test merge` combines the shards' `--json` reports into one document,
  rows in declaration order and the verdict the worst of them.

The port-conflict predicate is one implementation with two callers: the planner
groups by it, the scheduler serialises by it. Two copies would agree until one
learned about a port syntax the other had not — the reason `internal/nodedeps`,
`internal/cargotool`, `internal/download` and `internal/pathmatch` each exist.

## Considered and rejected

**An orchestrator process receiving results over webhooks.** It is a service:
an endpoint reachable from every runner, auth, availability, a lifetime, and a
network dependency `lydite test` does not have today. GitHub Actions already
solves aggregation — each job uploads its report, a final job merges them — and
lydite already emits exactly that document. A merge command is a function over
N reports; a webhook collector is infrastructure somebody has to operate.

**Naming the unit a pod.** Kubernetes' word for something lydite has no
relationship with.

**Naming the planner `lydite matrix`.** "Matrix" is one CI platform's
vocabulary, in a tool that deliberately hard-codes no container runtime and no
package manager. The plan is equally the local answer to "what would you run".

## Consequences

- The scheduler is a supported path everywhere, not a local convenience. It
  runs in CI at N>1 from the moment it ships.
- `--concurrency` is a flag and never a key in `.lydite/config.yml`. How many
  components a machine can run at once is a fact about the machine, and a 4-core
  runner reading a number committed from a 32-core workstation is the drift the
  flag avoids.
- Its default is 4 and is deliberately not derived from `NumCPU`. Every runner
  lydite drives is already internally parallel — `go test` fans out at
  `GOMAXPROCS`, `cargo nextest` runs tests concurrently, vitest forks workers —
  so `NumCPU` components at once oversubscribes the machine quadratically, and
  the symptom is timing-sensitive tests going flaky, which reads as a bad suite.
- `--keep-services` is removed. Its `down --volumes` teardown exists because a
  suite that truncates and reseeds is deterministic only if it starts from
  nothing, so keeping a stack alive made the *next* run start from the previous
  run's data — a silent non-determinism, and the only mechanism by which one
  run's port claims outlived the run. Bringing a compose file up by hand to
  debug one component is the workflow that exists with or without lydite.
