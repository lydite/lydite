# A shard reports what it owns, and the fold decides completeness

[ADR 0017](0017-shards-the-scheduler-and-the-planner.md) says a CI job holds a
**shard** — a set of components lydite runs in one process — and names the two
commands that distribute a run across shards and put the answers back together.
This is what those two turned out to be, and the three things that had to change
underneath them.

`lydite test plan` groups every declared component into shards and emits the
matrix. `lydite test merge` folds the shards' documents into one, and is the only
thing that computes the repository-wide coverage figures. Between them,
`lydite test` gains one rule that makes both possible: **a run reports exactly
the components it is responsible for, and nothing about any other.**

## The responsibility set

A run's responsibility set is its `--component` list, or the whole declaration
when there is none. It reports a suite row and a coverage row per component in
that set, a patch row for each whose files the change touched, and nothing at
all about a component outside it.

Before this, a `--component`-narrowed run still emitted a `coverage(<name>)` row
for every *declared* component, padding the ones it never ran with "not
selected". Under one process that is informative. Under a matrix it means every
shard publishes rows about components other shards are running, so the merged
document holds N answers per component and a consumer keying rows by label picks
one of them.

The rule buys the property everything else here rests on: **every declared
component appears exactly once across the shards.** So "did a shard die" is a
question about the declaration and the documents, and needs no third input to
answer.

`merge` therefore fails — rather than reporting `unmeasured` — when a declared
component has no row, when one has two, or when the shards' whole-tree gate rows
(`orphans`, `watch`, `select`) disagree. Failing is not severity inflation: an
`unmeasured` row does not vote, so a run whose runner died would publish
`"verdict": "pass"` over a repository it half tested. It is the same reason the
`schedule` row fails an interrupted run instead of leaving it amber.

## Completeness belongs to the fold, not to the run

`lydite test` refused to establish a baseline candidate whenever *any* declared
component had no entry — including components it was never asked to run. Under
`--affected` those were rescued by carrying the baseline's entry forward. Under
`--component` nothing rescued them, so a shard produced no candidate at all.

The tempting fix is to let `--component` mark unrun components carryable, and it
is wrong. Carrying is licensed by selection and by nothing else: affected
selection *proved* the change could not have touched that component, so the
recorded entry still describes it. `--component` proves nothing — the caller
asked for these — and carrying under it would attribute the merge-base's number
to a component this very change may have rewritten.

So the run refuses only over a component **it selected and failed to measure**,
and completeness is asked once, of the fold, against the declaration read from
the tree being recorded. `lydite test record` already did exactly that. The rule
now exists in one place instead of two, and the copy that is gone is the one that
could not be right for a shard.

The window this opens is real and bounded: between the run and the fold, a
partial document exists on disk. Nothing reads it but `record`, and `record`
refuses it by name.

## Only the fold can compute a repository-wide figure

`coverage(repo)` and `patch(repo)` are the two rows no shard can produce. Both
sum per-component counts, and both refuse to compare unless the baseline covers
every component in the figure — so a shard holding two of four components
computes them over its own two, and three shards publish three different answers
to a question about the repository.

A report's rows carry rendered prose, not numbers, so folding reports cannot
recover them. The shards' data document can: it already held each component's
counts and producer, and it gains each component's patch part and the baseline
entry that component was gated against.

A narrowed run therefore emits no repository-wide rows, and `merge` emits them
once. The composition itself is one implementation with two callers — the local
unsharded run and `merge` — for the reason `internal/scheduler`'s port predicate
is: two that agreed today would come apart the day one learned about a case the
other had not, and nothing would show it.

That is also why the document is renamed. `baseline.json` named half of what it
now carries, and it is a filename a command in another repository reads. Renaming
it to `measurements.json` is free today, because `lydite/actions` has no step that
consumes it yet ([#87](https://github.com/lydite/lydite/issues/87)); once one
exists the rename costs a coordinated release.

## The planner is pure, so the shards do the selecting

ADR 0017 wrote `lydite test plan --concurrency N`, with `--concurrency max`
putting one component in each shard and anything lower grouping them. **That part
is superseded.** `--concurrency` on `lydite test` means how many of a shard's
components run at once inside one process, and reusing the word for how many jobs
a matrix has gives one flag two meanings a reader cannot tell apart by name.

`plan` takes no such knob at all. A shard is a **conflict group**: the transitive
closure of `scheduler.Conflicts` — components sharing a published host port, or
rooted at overlapping directories. Components that conflict with nothing are a
shard of one. The grouping is the finest one that is safe, its size is a property
of the declaration rather than a number anyone tunes, and there is nothing to set
wrong.

Keeping a conflicting pair *together* rather than apart is the point, and the
reason is not the one ADR 0017 gives. Two matrix jobs on hosted runners are
separate machines, so two jobs binding 5432 do not collide there — but
self-hosted runners routinely place several jobs on one host, and then they do. A
shard is safe on any runner topology precisely because the scheduler serialises
inside it. That it also keeps the port lock contended in CI, which is ADR 0017's
stated reason for shards existing, follows for free: against
`lydite/proving-ground` the grouping is `{api, tally}` — the two components
publishing 5432 under deliberately different service names — plus `sdk` and `web`
alone.

`plan` reading the declaration and the compose files and nothing else is what
keeps ADR 0017's own claim true: no process, no state, no network. It is also why
`plan` cannot narrow by `--affected`, which needs a merge-base, git history and a
checkout that is not shallow. **The shards narrow instead**, running
`--affected --component <slice>`: `--component` says what this job is responsible
for, `--affected` says which of those need running, and the rest of the slice is
reported `not affected` with its baseline entry carried forward by the one shard
that owns it.

That combination was previously refused, on the grounds that a planner would emit
`--component` lists that were already the selected set. It would — but only a
planner that narrowed, and this one does not. The refusal went with the planner
that never existed.

## Considered and rejected

**A plan document that `merge` and `record` read back.** It answers "which shards
were expected" precisely, including by id, and it costs three things: `plan` gains
a git dependency and a shallow-clone failure mode, every downstream command gains
a third input, and a plan job that dies takes the definition of completeness with
it. Defining completeness against the tree needs no artifact to survive.

**Shards emitting partial repository rows, made honest by their counts.** A row
saying `2 of 4 component(s)` is not wrong, and three of them in one comment,
under one label, with three different verdicts, is unreadable. The counts make
partiality visible within one document; they do not make three documents into
one.

**Recording per shard.** N pushes racing one branch, and under the rule above
every shard's document is partial, so each would be refused. Recording stays one
write after the fold, in a job that runs no repository code
([#49](https://github.com/lydite/lydite/issues/49)).

**`plan --shards N` to cap the matrix.** A flag before anyone has the problem,
needing a bin-packing policy nobody has stated. GitHub's matrix limit is 256; a
repository with more components than that has a different problem.

## Consequences

- `merge` reads the declaration, so it needs `--dir`. Same dependency `record`
  has, for the same reason: the tree defines completeness, never the documents.
- `merge` touches no network. It composes from what the shards measured and from
  the baseline entries they recorded having gated against, so it needs neither the
  `lydite` branch nor a merge-base.
- `plan` writes no report document, alone among the commands. The rule that every
  command writes one exists so a *verdict* reaches the surface without depending
  on a redirection somebody remembered; `plan` reaches no verdict, and a section
  titled "plan" in a pull-request comment says nothing a reader can act on. Its
  matrix goes to `--out`, because stdout carries the report.
- `publish` is unchanged. A `merge` job that died renders as a section saying the
  directory held no document, which is already what it does.
- Recording is unaffected in shape: `lydite/actions` still needs a `record` step
  (#87), and now also needs the plan/matrix/merge shape, which is a second
  cross-repository cutover.
