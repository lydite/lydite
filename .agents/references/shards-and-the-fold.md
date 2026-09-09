# Shards, and the fold

> **The reference for `lydite test plan` and `lydite test merge`**, and for the CI matrix that consumes them.
>
> The declaration a shard is carved out of is in [`components.md`](components.md).

## Shards: `lydite test plan`, and the responsibility set

A CI job holds a **shard** — a set of components lydite runs in one process. `lydite test plan`
groups every declared component into shards and emits the matrix; `lydite test merge` folds the
shards' documents back into one. See
[ADR 0026](../../docs/adr/0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md).

**A run reports exactly the components it is responsible for, and nothing about any other.** The
responsibility set is the `--component` list, or the whole declaration when there is none: one suite
row and one coverage row per component in it, a patch row for each whose files the diff touched, and
no row at all about a component outside it. Under one process, padding a `coverage(<name>)` row for
every *declared* component is informative; under a matrix it means every shard publishes rows about
components other shards are running, so the merged document holds N answers per component and a
consumer keying rows by label
picks one of them.

The rule buys the property everything else rests on: **every declared component appears exactly once
across the shards.** So "did a shard die" is a question about the declaration and the documents, and
needs no third input to answer.

**`plan` is pure.** It reads `.lydite/components.yml` and each component's compose file, and nothing
else — no git, no network, no process — so it runs on a shallow checkout, on a fork, and on a machine
with no container runtime. That is also why it cannot narrow by `--affected`: the shards narrow
instead. A compose file it cannot read is an error rather than a component with no ports, because a
matrix built on unknown ports can put two components that contend for one port into different jobs,
which is the single thing the planner exists to prevent.

**A shard is a conflict group**: the transitive closure of `scheduler.Conflicts` — components sharing
a published host port, or rooted at overlapping directories. `internal/scheduler` owns that predicate
and the planner reads it rather than reimplementing it, for the reason `internal/pathmatch` has one
matcher. Components that conflict with nothing are a shard of one; the grouping is the finest one
that is safe, and there is nothing to set wrong. **There is no `--shards`, and no `--concurrency`** —
`--concurrency` on `lydite test` means how many of a shard's components run at once inside one
process, and reusing the word for how many jobs a matrix has gives one flag two meanings a reader
cannot tell apart by name.

Keeping a conflicting pair *together* is the point, and the reason is runner topology rather than
lock coverage: two matrix jobs on hosted runners are separate machines, so two of them binding 5432
do not collide there — but self-hosted runners routinely place several jobs on one host, and then
they do. A shard is safe on any topology precisely because the scheduler serialises inside it.

Shards are ordered by the declaration position of their first member and members in declaration
order, so two runs of one declaration emit an identical matrix — a shard's name is what its artifact
is called, and a name that moved would orphan the directory the fold reads. The name is the members
joined by `-`, unique because component names are. The matrix goes to `--out`
(`[{"name":"api-tally","components":"api,tally"}]`); stdout carries the report.

**`plan` writes no `.lydite-reports/plan.json`, alone among the commands.** The rule that every
command writes one exists so a *verdict* reaches the surface without depending on a redirection
somebody remembered; `plan` reaches no verdict, and a section titled "plan" in a pull-request comment
says nothing a reader can act on.

## `lydite test merge`: the fold decides completeness

`--dir` (for the declaration) and repeatable `--reports`. No network, and nothing from the repository
is executed.

**A repository that declares no components is an error, not a fold.** Completeness is a question
about the declaration, and an empty one answers every question with yes — so `merge` refuses it the
way `plan` and `scan` do rather than reporting that nought of nought components are covered.

**A declared component with no row from any shard is a failure, and so is one with two.** Not
`unmeasured`: that status does not vote, so a run whose runner died would publish `"verdict": "pass"`
over a repository it half tested. It is the same reason the `schedule` row fails an interrupted run
instead of leaving it amber. The `orphans`, `watch` and `select` rows ask about the declaration and
the tree, so every shard computes the same answer; they collapse to one, and a disagreement fails —
the shards did not see the same tree. The per-shard `schedule` rows fold into one
(`N shard(s), max K concurrent`) carrying each shard's serialised pairs beneath.

**`coverage(repo)` and `patch(repo)` are the two rows no shard can produce**, and `merge` is the only
thing that emits them. Both sum per-component counts and both refuse to compare unless the baseline
covers every component in the figure, so a shard holding two of four would answer about its own two
under a label about the repository. A run narrowed by `--component` therefore emits neither, and the
same holds for the bare `floor` summary row, which counts over the whole declaration.

A report's rows carry rendered prose rather than numbers, so folding reports cannot recover them —
the shards' `measurements.json` can, which is why it carries each component's patch part and the
baseline entry it was gated against alongside its counts. The composition is one implementation with
two callers, the unsharded local run and `merge`, for the reason the port predicate has one.

The fold's own `record` row is held against the declaration by the same `missingFromRecord` the
recording applies, so it cannot announce a landing `lydite test record` will then refuse. A run that
could not measure a component still hands on every component it *did* measure, carrying the refusal
as a reason: the document is the only channel by which a shard's counts reach the fold, and emptying
it would drop that shard's other components out of `coverage(repo)`, which is then composed over a
strict subset and rendered as a pass.

**Completeness belongs to the fold, not to the run.** `lydite test` refuses to establish a candidate
only over a component **it selected and failed to measure**. A component it never ran is simply
absent, and `record` asks completeness once, against the declaration read from the tree being
recorded. Carrying an unrun component's baseline forward stays licensed by affected selection and by
nothing else: selection *proved* the change could not have touched that component, where `--component`
proves nothing — the caller asked for these — and carrying under it would attribute the merge-base's
number to a component this very change may have rewritten. The window that opens is real and bounded:
between the run and the fold a partial document exists on disk, nothing reads it but `record`, and
`record` refuses it by name.

## A `watch` pattern that covers no file fails the run

Deliberately asymmetric with the orphan gate's excludes, which only warn. An exclude covering
nothing is fail-safe — it excludes nothing, so the gate stays stricter than declared, and
failing a build over tidying is how a gate earns a reputation for firing on ordinary work. A
`watch` covering nothing is fail-dangerous: the component stops being invalidated by its own
declared input, silently and permanently, while every run stays green. Same syntax, opposite
consequence, so the same treatment would be a false symmetry.

Two checks, in two places. `component.validate` rejects a malformed, empty, absolute or escaping
pattern at parse time, alongside `validateExcludes` — that gap existed for as long as `watch`
has, since only excludes were ever run through `pathmatch.ValidatePattern`. The `watch` row then
holds every pattern against the tree, because the dangerous typo is syntactically perfect:
`Makefil` is a valid pattern, and so is a bare `docs` written where `docs/**` was meant, since
these patterns are anchored and do not float. Outside a git repository — and equally when git
lists no file at all, which is what a scan root that is itself ignored looks like — it reports
`unmeasured` and passes, the shape `orphanRow` already has for both cases. A gate that saw no
files must not fail a declaration that is correct.

The file list is every path git knows about, not only source — a watch legitimately names a
`Makefile`, a `VERSION` file or an OpenAPI document, none of which any component could claim.
`gitdiff.Tracked` is that listing, shared with the orphan gate.

