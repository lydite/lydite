# Mutation reaches the ledger from a post-merge run over the merge commit's own diff

[ADR 0029](0029-the-ledger-appends-what-cannot-be-recomputed.md) put coverage, CRAP and test
counts in the quality history and left mutation out, for a structural reason rather than a
scoping one: a mutant exists only on a line the change touched, and on the default branch HEAD
is its own merge-base, so the one job holding a token that can push mutates nothing and has
nothing to record. Killed, survived and unviable are no more recomputable than the scalars
already in the ledger — after the merge there is no diff left to regenerate a mutant from — so
the gap is permanent for every change that lands while it stands.

This decides the route, what has to exist for lydite to be pointed down it, and what is recorded
when a run does not finish.

## The route, and why the other two are closed

[#49](https://github.com/lydite/lydite/issues/49) settled the shape of this in general terms,
and its conclusion is the constraint rather than an input:

> The measuring job runs the branch's code whatever token it holds. That is what measuring *is*.
> And a branch can edit both `.lydite/components.yml` (its `setup`/`teardown` shell, its runner
> args) and the workflow that invokes lydite. […] The recording job cannot verify what it is
> given without measuring it itself — at which point it is the measuring job again.

> Record **after** the change has merged, in a job on the default branch that measures the tree
> itself. The code it runs has passed review and the gate, so a writable token there is the risk
> any post-merge job carries rather than a new one, and the numbers are lydite's own rather than
> the branch's.

**Carrying the pull request's mutation document across workflows is that rejected shape.**
Reaching a pull request's artifact from the recording job — `run-id` with `actions: read`, or a
`workflow_run` trigger — hands the recording job a document produced by a job the branch could
have rewritten, and a command that executes nothing verifies nothing. There is one argument in
its favour that neither issue draws, and it is recorded here rather than left implied: the
ledger is append-only and **nothing is ever gated against it**, so a poisoned record corrupts a
history series rather than a verdict — unlike a poisoned coverage baseline, which every later
change then gates against. That is a real difference in blast radius and it is still not enough.
The baseline hole it is compared against is closed a different way and stays closed regardless,
and neither `run-id` + `actions: read` nor a `workflow_run` workflow exists anywhere in this
repository — so the weakest of the three options is the one that would introduce a whole class
of machinery to the CI surface.

**Landing it through the relay's identity is foreclosed.**
[ADR 0022](0022-a-vendor-operated-app-and-an-oidc-relay.md) deliberately does not grant the
relay `contents: write`; the relay comments, and a write token there would make the app the very
thing ADR 0025 took the write away from the measuring job to avoid.

**So: a post-merge mutation run, in lydite's own job on the default branch, scoped to the merge
commit's own diff.** This is the shape #49 endorses, and `.github/workflows/lydite-baseline.yml`
is already the workflow that measures a merged tree and records it.

The cost is plain and accepted: **mutation runs twice per change** — once read-only on the pull
request, where it gates, and once after merge, where it produces the numbers that are recorded.
[#97](https://github.com/lydite/lydite/issues/97) measured a 1,276-line change at 54 minutes for
one such run.

## The range is the first-parent diff, whatever the merge strategy

The post-merge run mutates `first-parent(HEAD)..HEAD`, always.

This is **not** an assumption that the repository squash-merges. For a squash commit, the first
parent is the mainline commit it landed on and the diff is the whole pull request. For a true
two-parent merge commit `M` with parents `P1` (mainline) and `P2` (the feature tip),
`git diff P1 M` is exactly what landing `M` changed on the mainline — the branch's whole
contribution plus whatever the conflict resolution decided — which is the same semantic content
the squash commit's diff carries. The two strategies agree under first-parent diffing, so there
is nothing to detect and nothing to refuse.

Detecting a merge commit and refusing it was considered, and lost once those semantics were
worked through: it would buy no correctness and would make the recording work only for
repositories that squash. lydite serves repositories that do not, and a tool whose history
silently stops at the first merge commit is worse than one that never recorded mutation at all —
the series would simply go quiet with every gate still green.

## An explicit base revision, not a branch to fetch

`lydite mutation` gains **`--base-sha <revision>`**, mutually exclusive with `--base-branch`.
Supplying both is a usage error: they are two answers to one question, and picking a winner
silently would let a mis-wired workflow mutate a range nobody asked for.

It takes anything `git rev-parse --verify <revision>^{commit}` resolves — a full or abbreviated
SHA, or a relative ref such as `HEAD~1` — and it **fetches nothing and computes no merge-base**.
That is the whole reason it cannot be spelled as a value for the existing flag.
`gitstate.ResolveBaseSHA` treats its argument as a branch name: it fetches `origin/<branch>` and
takes the merge-base of HEAD against it. Those are the right semantics for a pull request, which
is asking *where did I diverge*, and the wrong ones here, where the caller is asserting *this
exact commit is the base*. On the default branch a merge-base against the default branch is HEAD
itself, which is how mutation came to have nothing to say there in the first place.

The flag reaches **both** the mutant-generation range and `selectAffected`. They resolve the
base independently today, and leaving one of them on the branch path would have `--affected`
answer about a different range than the mutants were generated against — a component reported as
untouched while its own mutants were being run, or the reverse.

It is named `--base-sha` and not `--diff-base`. `lydite scan` already has a `--diff-base`, which
resolves differently; giving two commands the same flag spelling for two resolution rules invites
a reader to carry one command's meaning into the other, and the two are exactly as far apart as
a fetch and a merge-base.

## `mutants.json`, beside the rendered report

The counts exist today only as rendered prose. `mutation.json` is a `ui.Document` — rows of
status, label, value and detail — so a fold can recover "3 ran out of memory" as a sentence and
not as a number. `mutation.Summary` carries the scalars and has no JSON tags at all; it is never
serialised.

So a run writes a second, typed document beside the rendered one. This is the precedent
`measurements.json` set for exactly this reason, and it follows it in shape: the document names
**the tree it measured**, bound the same way `measurements.json` binds its tree, which is the one
integrity property available to a recording command that executes nothing
([ADR 0025](0025-a-baseline-records-its-producer-and-only-record-writes-it.md)). Beside the tree
it carries each component's `mutation.Summary` scalars and whether that component ran at all.

The rendered `mutation.json` stays byte-identical. This is additive: a consumer reading the
report today reads the same bytes after.

It is called `mutants.json`, not something with "mutation" in its name. The glossary already
draws the line the name has to respect — *the mutation is the act, the mutant is the artefact* —
and this document reports counts of artefacts. The name also has to survive being read next to
`mutation.json` in a directory listing and in a workflow's `find`, which a near-homograph would
not.

**A component that did not run is absent from the document**, never present with zeros. Untouched
by the diff, declared `mutation: false`, or in a shard that never reached it — all three are the
same answer, which is that nothing measured this component. `lydite mutation merge` folds this
document across shards the same way it folds the rendered report, and a shard that ran nothing
for a component contributes nothing for it rather than contributing zeros that would win the
fold.

The document therefore holds exactly what was reported for the run that wrote it: nothing more,
and nothing less. A job-level timeout that killed the run after some components had completed
still reports and records those that completed. That is not a special case carved out for
timeouts — it is the same rule as every other partial run.

## `Component.Mutation` is one struct, and absent is not zero

The ledger gains `Component.Mutation *Mutation`, carrying killed, survived, unviable, timed-out,
out-of-memory and acknowledged. Acknowledged belongs beside the other five for the reason any of
them is here at all: a `//lydite:equivalent` declaration is a fact about a mutant that existed
only on a line the change touched, and after the merge there is no diff left to re-derive it
from. It is also what makes a reader's `Total()` reconstructible from the ledger later, rather
than an undercount silently missing the mutants a declaration took out of the denominator.

**One pointer to a struct, not six independent `*int` fields.** The six counts of a mutation run
always arrive together, out of one `mutation.Summary`: there is no run in which killed is known
and survived is not. Per-field optionality would encode a granularity the data never has, and
every reader would then have to decide what a record with three of six fields means — a question
nothing can produce.

The outer pointer is nil for a component the merge commit's diff did not touch, for one declared
`mutation: false`, and for one whose run did not complete or report. A component that ran and
killed every mutant records the struct with a survived count of zero: a measured zero, and a
different fact from absence. This is the rule ADR 0029 already states for every metric on a
component — *every metric on a component is optional, and absent is not zero* — applied
unchanged.

**No version bump.** ADR 0029 settled that a ledger gains a field the way nothing else in lydite
does: nothing is ever compared against a stored record, so a field added later costs a series
that starts on the day it was added, which is what an append-only ledger is for.

## A run that did not finish records nothing for what it did not measure

A component whose post-merge run failed or was cut short records **no** `Mutation` field, and the
reason appears on its report row and in the job log, so *did not run to completion* is never
readable as *ran, and the suite killed everything*.

This is the repository's own standing rule, and it is the rule rather than a preference here:

> A gate that could not run never renders as one that passed. […] a workflow that forgot to ask
> for a gate otherwise reports exactly the green of one that ran it.

Zeroing an incomplete run writes a perfect suite into a permanent history, and the ledger is
append-only — there is no later measurement of that commit's diff to correct it with, because
after the merge the diff is gone. The honest absence is recoverable by a reader; the invented
zero is not recoverable by anyone.

## The job, its cost, and declining it

The post-merge `mutate` matrix job carries the **same 60-minute `timeout-minutes` as the existing
pull-request `mutation` job** in `.github/workflows/lydite-pr.yml`. It is the same command over a
comparable diff, so it is the same expected cost band, and #97's 54 minutes was measured against
that same ceiling. Inventing a second number here would assert something nothing measured.

**`--affected` applies, exactly as it does on a pull request.** The merge commit's own diff is the
scope either way, and there is no reason to mutate a component the merge did not touch merely
because the run is now happening on the default branch.

The baseline workflow's concurrency group is already serial (`cancel-in-progress: false`), so a
slow post-merge run delays the *next* merge's recording rather than racing it or being cancelled
by it. That is accepted: recording is append-only and eventually consistent, and nothing is ever
gated against it, so a late record is a late point on a line and never a blocked merge.

**A consumer's only lever to decline is the one that already exists**: the per-component
`mutation: true` opt-in in `.lydite/components.yml`. No CI-level toggle is added. A second way to
express a decline alongside the first is two settings that can disagree, and a reader then has to
check both to answer whether a component is mutated. A repository with no `mutation: true`
component pays a fast no-op run, which is what it already pays on every pull request.

## Consequences

- Mutation is measured twice per change. The pull-request run gates and records nothing; the
  post-merge run records and gates nothing.
- The mutation series for a component starts on the day this lands, per ADR 0029's rule for a
  gained ledger field. Nothing backfills it: the diffs those mutants would have come from no
  longer exist.
- `lydite/actions` needs the post-merge job before its `@v1` moves, or a consumer wiring the
  reusable workflow gates on mutation and records none of it — indistinguishable, in the history,
  from a repository that declared no mutated component.
- The amendment to ADR 0029's "Mutation is out" paragraph is recorded there as a note; the
  narrative lives in
  [`quality-history.md`](../../agentic/references/quality-history.md).
