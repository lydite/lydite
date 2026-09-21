# A component declares the paths it occupies, and the scheduler locks them

`internal/scheduler` holds two kinds of lock while an item runs: each host port
its compose services publish, and its own directory tree, by containment rather
than equality. Both are physical — the second item would fail to bind the port,
and two items writing into one tree install into, build in and write their
output to it at once.

There is a third thing a component writes into, and neither lock covers it: a
directory outside its own root that its `setup:` builds. `occupies:` is how a
component names those directories — a list of paths relative to the scan root —
and an item's occupied set is its `dir` plus every path it declares. The
scheduler compares the two sets whole, by containment in either direction, so a
root inside somebody else's occupied path and two occupied paths that overlap
are the same conflict as two overlapping roots. `lydite test plan` groups by the
same predicate ([ADR 0017](0017-shards-the-scheduler-and-the-planner.md)), so a
contending pair lands in one shard rather than in two jobs a self-hosted runner
may place on one host.

The case is `tandiko-design`. `ui` is rooted at `packages/ui` and `storybook` at
`apps/storybook`; neither root contains the other and neither declares a
service, so today the scheduler sees no conflict at all and runs them together.
Both carry the same `setup:` line — `pnpm --filter @tandiko/tokens run build` —
because both need that package built before their suites can import it. `tsup`
cleans its output directory before it writes, so on a cold tree the second build
removes the directory the first has half-populated and whichever suite got there
first fails with `ENOENT` on a file that existed a moment ago. The row blames
the component's tests.

## Doing the work once is right for the install, and wrong here

The standing answer to a shared-output collision in this codebase is not a lock.
A workspace root above several components is written by every one of their
installs, and `internal/nodedeps` resolves that root and installs it **exactly
once per process** — the first component to reach it runs the frozen install and
every other one waits and then finds it already done. Nothing there needs to
know a workspace root exists, because nothing races to write it twice. That is
the better instrument where it applies: a lock costs concurrency to make a race
safe, where doing the work once removes both the race and the duplicated work.

It does not apply here, and the reason is not preference. lydite **orchestrates
the install itself**: it resolves the root by walking up to the nearest
recognised lockfile, chooses the package manager from which lockfile it found,
and issues the invocation. "The same work" is a value lydite computes — one
resolved root — so it can name it, do it once, and make everyone else wait on
it.

A `setup:` line is not lydite's work. It is opaque shell, handed to `sh -c` in
the component's own directory with the component's own environment, and lydite
knows nothing about it beyond the string. It cannot tell that `pnpm --filter
@tandiko/tokens run build` in `ui` is the same work as the identical line in
`storybook`, because it cannot tell what either line does at all. **It cannot
deduplicate what it cannot parse.**

**Deduplicating identical setup strings was rejected**, which is the shape that
looks closest to the install. It is wrong in both directions. Two identical
lines are not necessarily one piece of work — `make migrate` against each
component's own database is per-component work that must run twice, and running
it once would leave one component's schema unmigrated. And two *different* lines
routinely are one piece of work: the same build reached through a different
script name, a `make` target wrapping the `pnpm` invocation, an absolute path
where the other used a relative one. String equality answers a question nobody
asked; the question it stands in for needs a parser for every package manager's
command line, and then for every Makefile behind them.

So locking is the only instrument left, and the declaration is the only source.
The author of a `setup:` line is the one party who knows what it touches.

## Declared, because nothing lydite reads can derive it

The workspace-root case is derivable and this one is not, and the contrast is
the whole argument for a key. A workspace root is a function of `dir:`:
`nodedeps.WorkspaceRoot` walks up from the component's root to the nearest
ancestor holding a lockfile, bounded by the scan root, and the answer is in the
tree. An occupied path appears in no file lydite reads. It is in the shell
`setup:` runs, or in a `package.json` script that line invokes, or in a `tsup`
config that script reads.

**Locking the workspace root instead was rejected** — the one derivable
candidate, and the wrong lock even when it is available. A monorepo has one
lockfile at its root, so that lock is held by every component under it: `ui`'s
whole run would serialise against `storybook`'s whole run, and against every
other component in the repository, because two of them share two lines of
`setup:`. The repository's parallelism would collapse to one at a time on a
declaration that named nothing. `occupies:` names the tree the pair actually
contends for, so the pair serialises and nothing else does.

**Requiring the path to exist was rejected.** `dir` must exist, and an occupied
path must not. The directory a `setup:` builds is absent on a cold checkout,
which is precisely the state the collision happens in — holding the declaration
to an existing path would refuse it on the run that needs it. The asymmetry is
in what each failure costs: a `dir` that is not there is a component nothing
runs while the build still reports green, and a path nothing is at costs one
pair of components their concurrency and nothing else.

## Not `depends_on:`, and not `watch:`

**`depends_on:` is an invalidation edge and orders nothing.** It exists for
affected selection: a change to the dependency makes the dependent run.
`TestDependsOnDoesNotSerialise` holds it out of the scheduler deliberately —
lydite passes no artifact between components, so ordering them would cost
parallelism to express a claim their author never made, and would need a policy
for what a dependent does when its dependency fails. It also cannot state this
relation: `ui` and `storybook` do not depend on each other. They contend,
symmetrically, over a third thing.

**`watch:` is a list of globs read only by `internal/affected`**, and it means
"a change here invalidates me" — a statement about the past, matched against a
diff. An occupied path is a statement about the present: "this subtree is mine
while I run". Reusing the key would make the scheduler's exclusivity decision
depend on pattern matching — asking whether two glob lists could match some path
in common, which is a different computation from directory containment and has
no single answer — and it would serialise every component watching `Makefile`
against every other one, for a file none of them writes.

## The scheduler locks what is physical, and this is not ordering

`occupies:` says nothing about sequence. Two components sharing a path run one
after the other, in whichever order the scheduler's admission finds them free;
nothing in the declaration says which goes first, and nothing reads it as
though it did. The conflict is symmetric, as a port conflict is, and the
`schedule` row names the pair and the tree they share rather than a direction.

This is also why the key holds the further paths in one list with `dir`, once
the item is built. A lock on a tree is a lock on a tree: which of the two a path
was declared as decides nothing about what a second component writing into it
would do.

## Consequences

- `.lydite/components.yml` parses with `KnownFields(strict)` — an unknown key is
  a hard parse error, because a silently dropped key means a component
  configured differently from what its author wrote while every run still
  reports a result. A repository that declares `occupies:` therefore fails to
  parse under any lydite that predates the key, on every command that loads the
  declaration, with an error naming an unknown field. Adopting the key pins the
  repository to a lydite that knows it. **There is no schema-version signal to
  catch that skew**: the declaration carries no version, so an older binary
  reports a field it does not recognise rather than a version it is short of,
  and a consumer whose CI pins one lydite and whose developers install another
  sees it as a broken file.
- Over-declaring is safe and costs concurrency alone. A path nothing writes is a
  lock nobody contends for until some other component declares it too, and then
  the two serialise for no reason — visible in the `schedule` row, which names
  every pair that shared something.
- A path declared twice within one component is refused rather than folded away.
  It locks nothing more than once, so the second entry is either a mistake or
  two spellings of one directory read as two statements. Across components it is
  the point: shared declarations are what produce the lock.
- Entries are cleaned at parse time, so `./tokens`, `tokens/` and `tokens` are
  one directory and the scheduler compares directories rather than spellings. An
  absolute path, a `~` prefix, and a path escaping the scan root are refused.
- The paths are scan-root-relative, like `watch:` and unlike `compose.file:`.
  The scheduler compares one component's declaration against another's, and two
  component-relative paths are not comparable without first resolving both.
- Shards get coarser in a repository that declares shared occupancy: the pair is
  one conflict group and runs in one job. That is the same trade a shared host
  port already makes, and for the same reason — the grouping is the finest one
  that is safe.
- `scheduler.Item` gains the field, and **both** constructions of an `Item`
  carry it: `itemFor` in `cmd/lydite/test.go` and `planItems` in
  `cmd/lydite/plan.go`, held to the same conflicts by a test. A field one of the
  two left out is a contending pair the matrix splits across jobs with nothing
  to serialise it, and the planner is the half no local run exercises.
  `lydite mutation` inherits the lock through `itemFor`.
- The declaration is a claim its author writes, and it may only ever *add* a
  lock. Nothing it says makes a gate greener: an over-declaration costs the run
  parallelism and an omission leaves the race that already exists, so
  [ADR 0014](0014-evidence-only-referral-matching.md)'s rule about
  author-controlled claims is not engaged by it.

> The scheduler's side of this is in
> [`services-and-scheduling.md`](../../agentic/references/services-and-scheduling.md);
> the key itself is in
> [`components.md`](../../agentic/references/components.md).
