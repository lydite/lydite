# Services, and the scheduler

> **The reference for `internal/compose` and `internal/scheduler`** — the services a suite needs, and what may run beside what.
>
> The declaration these read from is in [`components.md`](components.md).

## Services: a compose file, and no schema of lydite's own

`internal/compose` starts what a component's suite needs and stops it again.
lydite owns **no service schema**: images, ports, environment and healthchecks are
compose's job already, and a second description here could only agree redundantly
or drift.

It hard-codes **no container runtime** either. Which implementation is present is a
property of the machine, not of the repository — podman on a laptop, docker on a
runner — so `Probe` tries `docker compose`, `podman compose`, then the standalone
`docker-compose`, and the run says which it chose on stderr. It *runs* each
candidate rather than looking it up on PATH: `docker` exists on a machine whose
daemon is stopped and on one with no compose plugin, and neither can start a
service. A component declaring no services is never probed at all, so a repository
without services runs on a machine with no container engine.

**`wait: healthy` requires a declared healthcheck, and is refused without one**
rather than degrading to `started`. A suite racing a database that is not yet
listening is the flakiest thing a pipeline can contain, and the failure is
attributed to the test rather than to the wait. It is also the default, so a
component that says nothing gets the strict answer. The waiting itself is compose's
`--wait`, not lydite polling `ps`: the healthcheck in the file is what defines
ready, and a second implementation here would be lydite deciding what healthy
means. `started` and `none` produce the same invocation, because `up --detach`
already returns only once every container has started — the distinction is the
repository's statement of intent, and `healthy` is the one that changes the command.

**Teardown runs on every path out**, including a `setup` that failed halfway, which
is when a half-applied migration most needs undoing. It is `down --volumes`: a suite
that truncates and reseeds is deterministic only if it starts from nothing. Both
teardowns get a context of their own, because the run's may already be cancelled and
a cancelled teardown is the leak this exists to prevent. A failing `teardown` command
turns a passing component into a failing one — it has left state the next run
inherits — but never masks a failure that already happened, since the earlier one is
what the reader has to act on.

Each component's stack gets its own compose project (`lydite-<name>`), so two
components cannot adopt each other's containers and teardown removes exactly what
this run started. Published **host ports** are read while the file is open, in both
of compose's port syntaxes: the scheduler serialises two components that publish the
same one, and the conflict is physical — two services called `db` on different ports
do not collide, and a `db` and a `postgres` on the same port do.

`Stack.HostPorts` follows each named service's `depends_on` closure, because
compose does: `compose up app` starts everything `app` depends on, and a `db`
pulled in that way publishes its host port just as surely as one named
directly. A lock computed from `compose.up` alone would leave that port unheld,
and the next component to want it fails mid-run on the bind error the lock
exists to prevent.

`compose.Load` probes for a runtime; `compose.LoadWith` takes a
`RuntimeSource` the caller supplies. It is a function rather than a `Runtime`
so that it is consulted **after** the declaration has been checked: probing
first masks every declaration error behind the state of the machine, so a
`compose.up` naming a service the file does not declare reports an absent
container runtime instead — and `internal/compose`'s own tests then pass or
fail depending on whether the developer running them happens to have docker
installed. `lydite test` loads every selected component's stack before any of them starts,
because a stack is the only thing that knows its component's ports and the scheduler
needs all of them before it can decide what may run together — so the probe happens
once for the run rather than once per component, and compose's own validation lands
before the first container instead of mid-run. A run whose components declare no
services probes nothing at all.

Unknown keys in a compose file are **accepted**, unlike everywhere else lydite parses
YAML. The file is compose's, not lydite's, and rejecting a key lydite has no opinion
about would make lydite's version the ceiling on what a repository may write in a
file lydite does not own.

## The scheduler: ports are the only thing that serialises

Components run concurrently. `internal/scheduler` bounds how many at once and
holds a lock on what a component occupies while it runs: each host port its
compose services publish, and its own directory tree. Two components sharing
either run in sequence — the directory by containment rather than equality,
since a component at the repository root and one rooted at `web/` are writing
into the same files. Both conflicts are physical — the second would fail to bind
the port, and two components rooted at one tree install into, build in and
write their output to it at once, where an `npm ci` removing and recreating a
`node_modules` another is importing from is not a race either suite can report
honestly. `component.validate` enforces unique names, not unique directories,
so a repository may legitimately declare two components over one root. It takes plain data — an item
is a name and a set of ports — and the caller supplies the function that runs one,
so the constraint is testable without a container runtime and the port-conflict
predicate has one implementation rather than one here and another in the planner
that groups shards ([ADR 0017](../../docs/adr/0017-shards-the-scheduler-and-the-planner.md)).

**A CI job holds a shard — a set of components — not a single component.** Under
one component per job nothing in CI ever contends for a port, so the lock's only
exercise would be a local run somebody has to remember to do. `ci-end2end.yml`'s
proving ground job runs all four components in one process, which is what gives
the lock an automated end-to-end assertion at all.

**`--concurrency` defaults to 4 and is deliberately not derived from `NumCPU`.**
Every runner lydite drives is already internally parallel — `go test` fans out at
`GOMAXPROCS`, `cargo nextest` runs tests concurrently, vitest forks workers — so
one component already tries to use the whole machine and `NumCPU` of them
oversubscribe it quadratically. The symptom is timing-sensitive tests going flaky,
which reads as a bad suite rather than as a bad bound. It is not 1 either: a
scheduler that never runs two components at once passes every assertion about port
locks without once having taken one. `max` is one slot per selected component. It
is a flag and never a key in `.lydite/config.yml`, for the reason a repository
does not state how many cores a machine has.

**`depends_on` is not a scheduling input.** It is an invalidation edge, declared
for affected selection; lydite passes no artifact between components, so ordering
them would cost parallelism to express a claim their author never made — and would
need a policy for what a dependent does when its dependency fails, which is new
surface bought with nothing. `TestDependsOnDoesNotSerialise` holds it.

**An interrupted run fails through the `schedule` row.** The rows for
components that never started are `unmeasured`, which does not vote, so a run
cut short by a CI job timeout would otherwise carry no failing row — and
`--json` would publish `"verdict": "pass"` for a run that tested half the
repository. Anything automated reads that document and never the terminal, so a
truncation visible only in the process exit code is one the PR comment renders
green. The `schedule` row is where it lands, because it is the row about the run
rather than about any component, and `ui.Report.ExitCode` stays the single place
the verdict-to-exit-code mapping lives.

**A failure never cancels the rest.** `lydite test` is a gate, and somebody
clearing one wants every failure in a single run rather than N runs paying the
container startup each time. GitHub Actions already has `fail-fast` on the matrix,
at the layer that can cancel other machines.

**Rows are in declaration order, never completion order.** Workers write into
their own slot and the rows are added after the wait, so two runs of the same
declaration produce the same document; completion order would put this run's
timing into it.

**An interrupt cancels the context rather than killing the process.**
`newTestCmd`'s `RunE` installs `signal.NotifyContext` — **in the command, not in
`main.go`**, because `test` is the only command that reports a cut-short run
honestly. `scan` and `coverage` would render every killed tool as a finding and
publish a document saying every scanner failed, so they keep dying on the signal.
It is scoped here so a component already running reaches its deferred teardown: a
signal that skipped those defers leaves one stack per started component holding
the ports the next run has to bind, and the leak surfaces as an unrelated failure
one run later. The handler unregisters as soon as the first signal lands, so a
second interrupt gets the default disposition and kills a teardown that has
itself hung.

**Nothing about a cancelled run is reported as a result.** The scheduler starts
nothing further once the context is done, and a component it never reached is
`unmeasured`/`not run` rather than dropped — a truncated run that omitted rows
would read as a complete run over fewer components. A component that *had*
started is killed mid-suite and exits non-zero, so it becomes
`unmeasured`/`not completed` too: under cancellation lydite cannot tell a suite
that failed from one that was killed, and four red rows blaming a CI job timeout
on the repository's tests is the worst available answer. Only a component that
had already passed keeps its result, because that one is not in doubt.

**The `schedule` row is how the run says what it actually did.** It carries the
maximum concurrency the bound and the locks allowed, and names each pair that
shared one. That number is the point — every assertion about port locks is
satisfied by a run that never had two components going at once, so a report that
cannot distinguish the two proves nothing. It counts admissions rather than
goroutine entries, because counting when a goroutine actually starts would let a
correct run report 1 and make the CI assertion below flaky; that two components
genuinely overlap is forced in `internal/scheduler`'s own tests, by a barrier
none of them can pass alone. `.github/assert-proving-ground.py` holds the
proving ground to a maximum of at least 2 and to having serialised `tally` and
`api` on 5432 — the components rooted at `rust/` and `go/api/`, which publish it
under services deliberately named `postgres` and `db`, so a lock keyed on
service names fails there. The row names components, never directories or
services, which is the only one of the three that is unique by construction.

