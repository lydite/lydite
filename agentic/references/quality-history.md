# Quality history: the ledger and the projection

> **The reference for `internal/ledger` and `lydite test record`.**

A baseline is a **cache** and quality history is a **ledger**, and the terms exist to keep
the difference drawn. A baseline is regenerable by re-running the tool over the same tree,
so a write that never lands costs a slower run and no information, and a better measurement
of one tree overwrites the last. A ledger cannot be recomputed at all — the pins that
produced a number may have moved since, and after a squash merge the commit it describes no
longer exists — so a record is appended, never edited and never deleted. See
[ADR 0009](../../docs/adr/0009-quality-history-storage-and-access.md) for the design and
[ADR 0029](../../docs/adr/0029-the-ledger-appends-what-cannot-be-recomputed.md) for what building
it decided.

```
history/v1/2026-09.ndjson          the month's records, one JSON object per line
history/v1/2026-09.1.ndjson        the same month, once the first part is full
history/v1/daily/main/2026.ndjson  the downsampled projection, per branch
```

**One writer and one commit, over two policies.** `gitstate.Write` takes a snapshot and the
records together, so the single call site stays the one place anything reaches the branch.
A second *writer* is what that invariant forbids; a second *commit* is not the same thing
and was still rejected, because it doubles the retry loop against a busy shared branch and
leaves a window where the branch holds a baseline with no record beside it — a hole in the
history lydite's own write path invented.

**Staging happens inside each push attempt, not once ahead of the retry loop.** A baseline
document is the same bytes whatever the branch holds; a record is appended to the partition
on the branch *as fetched*, and a retry fetches a branch a concurrent run may have advanced.
`gitstate.Records` is a function for that reason, and it is handed the staging worktree.

**The two are refused at different times, and that is the whole of the distinction in
practice.** A baseline is refused whenever it would be partial, since any non-empty entry
reads as a cache hit and a partial one gates every later change on nothing. A record is
appended whether or not what was measured adds up to a baseline. **A commit whose suite went
red establishes no baseline by construction and carries the counts a history most wants** —
how many tests ran and how many failed — so `lydite test record` appends it and reports
`record` as unmeasured in the same breath. The rule that only a passing component
contributes a measurement is about a measurement *of the tree*; a JUnit report says what
happened, which is exactly what a ledger records.

**A gap is written by the next successful append, never by the failed one.** The obvious
mechanism cannot work: the append that would consume a sequence number is the append that
failed, so nothing wrote and nothing was consumed. Two mechanisms replace it, and the split
is the point:

- **Every record names its commit's first parent**, so a reader that finds a record whose
  parent is not the previous record's commit has found the break using nothing but the file
  it is already reading. Nothing the failing run did can damage this — it is written by the
  *next* run, out of git history.
- **An explicit `gap` record** says how many commits lie between the last recorded one and
  this one along the first-parent chain. That is the half a reader cannot work out alone,
  and it needs the repository, which the recording job has.

A width that cannot be established — a force-push, an unrelated history, a checkout too
shallow — is recorded as unknown rather than guessed. The first record on a branch is never
a gap: nothing precedes it, and claiming one would put a break at the start of every line.

**The ledger's completeness under concurrent writers comes from absorbing overlap, not from
serializing it.** Any pipeline that writes to the state branch must not rely on its own
`concurrency:` group to keep writers from overlapping — GitHub keeps only one pending run per
group, so a group shared across triggers evicts a later trigger's still-queued run before it
starts, silently, with no later run left to recover what it would have measured. A group scoped
instead to the key that needs a guaranteed run (see
[`scope-a-concurrency-group-to-what-must-not-evict-it`](../rules/scope-a-concurrency-group-to-what-must-not-evict-it.md))
lets different keys run at once rather than queue, which only works if what they write to can
take the resulting overlap. `gitstate.Write` (`source/cli/internal/gitstate/gitstate.go`) is
built to: each attempt fetches the state branch fresh and stages against that tip, so a push
rejected by a concurrent writer's own push is retried against the new state rather than
swallowed. Concurrent writers therefore all land, or, when one loses the race on every attempt,
that loss is never silent: `Write` returns an error rather than a recording, and the next
successful append reads its own newest record's parent and writes an explicit `gap` rather than
the two points joining as if nothing were missing. The retry cap bounds how much overlap one
writer survives, not whether the ledger's completeness guarantee holds — more writers in flight
at once than the cap allows makes a `gap` more likely, not the guarantee false, so a `gap` is
expected to stay rare rather than to disappear.

**An entry is keyed by commit; a baseline is keyed by tree.** Different keys for different
reasons, and copying one onto the other would be a reflex. A baseline answers "what was
measured for this content", which is why a pull request and the commit it becomes
deliberately share one; history is a sequence of events, and two commits carrying one tree
are two points on the line. A record carries the commit, its first parent, the branch and
the tree — the tree so it can be joined to the baseline for the same content.

**The branch is stated before it is discovered.** `lydite test record --branch <name>` is the
caller's own statement, and a checkout that names no branch is the normal shape of a CI job —
one pinned to a SHA, one that moved to a base commit to measure it. Discovery
(`git symbolic-ref`) answers nothing there, and a guess is worse than nothing: a record filed
under a branch this checkout is not on puts one line's points on another line, and nothing
downstream can tell. So both workflows pass it, the same ladder `--base-branch` follows, and a
run that can name no branch appends nothing and says which flag fills the gap.

**The timestamp is the commit's own committer date, never the clock at append time.** The
partition is named from it, so a re-recording lands where the first attempt would have put
it and the deduplication finds it there; and a CI job that ran hours late does not look like
a delayed commit. A record's identity is its commit, its **branch** and its kind, so a
retried push, a re-run workflow and a second invocation leave one line — and one commit
recorded on two branches leaves two, since it is a point on each of them and a release
branch cut from the default one would otherwise start its history wherever it next
diverged.

**Scalars are per component, and the repository figures are sums nobody stores.** The
component is the unit every gate reports at, and a repository-wide number cannot say which
component moved. The one thing stored beside the components is a root-scoped gate's finding
count, and it is not a sum over them but a separate measurement — see below. Every metric is a pointer, so absent and zero are different answers: a
component with no function above the CRAP threshold records zero and one lydite scores none
of records nothing.

**A ledger gains a field the way nothing else here does.** A baseline directory must be
versioned on a gained field, because an entry lacking it would still read as a cache hit —
the trap `crap/v1` is a separate directory to avoid. Nothing here is compared against a
stored record, so a field added later costs a series that starts on the day it was added,
which is what an append-only ledger is *for*.

**A month grows parts; the granularity does not change.** Every file must be independently
fetchable under the GitHub Contents API's 1 MB cap, because the dashboard reads this branch
directly with the viewer's own credentials. A busier repository rolls to `2026-09.1.ndjson`,
*before* the write that would cross the line rather than after it, so no part is ever over
the cap even momentarily. The projection is one file **per branch per calendar year**,
holding one row per day: the last record of that day, plus how many records and gaps the day
held. Per branch because a day's row carries a scalar per component and several branches
recording into one file would walk towards the same cap — and because a chart reads exactly
the branch it is drawing. Per year because that is what makes the bound hold by construction
rather than by expectation. The last record and not a mean, which is a number no commit ever
had. There is no index file: the directory listing is the index, and an index is state that
can disagree with the files it names.

**Mutation is one struct, not seven independent scalars, and it comes from `mutants.json`.** A
mutation run's killed, timed-out, out-of-memory, survived, unviable and acknowledged counts, and
its elapsed time in seconds, always arrive together from one component's run and its
`mutants.json` entry, so `Component.Mutation` is a single pointer rather than seven optional fields — a record with three
present is a question nothing produced. Nought or absent elapsed time means not recorded. The
ledger version is not bumped for it: a field added later starts a series on the day it was added.
It is absent for a component the recorded commit's own diff did not touch, for one declared
`mutation: false`, and for one whose run did not complete, and present with a measured zero
wherever a component ran and killed every mutant — the same rule every other metric here follows,
applied to a run that only exists because the recording commit is the post-merge run's own diff to
mutate. See [ADR 0043](../../docs/adr/0043-mutation-reaches-the-ledger-from-a-post-merge-run.md),
resolving [#112](https://github.com/lydite/lydite/issues/112) and the shape [#49](https://github.com/lydite/lydite/issues/49)
settled for it.

**Recording never read the exit code, so `--no-gate` changes nothing about what reaches the
ledger.** A `mutate` matrix job invoking `lydite mutation --no-gate` feeds its `mutants.json` to
`lydite test record` exactly as it would bare — a survivor is recorded either way. What the flag
changes is the workflow around that recording: a survivor on the merge commit no longer turns the
recording run red, since nothing there gates on it any more. See
[ADR 0048](../../docs/adr/0048-a-post-merge-mutation-run-records-its-survivors.md).

**A finding count is per gate, and that is not a refinement of "per component" — it is the only
shape that can say what there is to say.** A gate has a third state the other scalars do not: it can
apply and have found nothing, or not apply to this component's language at all. A key present at `0`
is the first; a key absent is the second. One integer collapses them, and a series that read an
absence as a zero would draw a clean line through a scanner that never ran — the failure "absent is
not zero" exists to prevent, in the metric most likely to hit it. Scanner gates only: `CRAP.Above`
already *is* the count of CRAP findings, and recording it twice makes two quantities that must agree
into two quantities free to disagree.

**The count comes from the document and the gate set comes from the tree.** A clean gate reports no
findings, so the claims alone cannot tell a component gosec found nothing in from one gosec never
looked at; the declaration and `.lydite/config.yml` say which gates each language implies, and they
are read from the tree being recorded for the same reason the baseline's completeness check is. Each
language package names its own gates, so the set cannot drift from the checks it runs, and a gate's
name is one constant shared with the row it labels. Reading the set out of the scan's rows would
answer more cases and cost what findings-as-data exists for — `gosec(cli)` parsed back into a gate
and a component is the text-scraping the channel removed. **A recording that read no scan document
records no count at all**, never a nought per gate: a nought says the gate ran.

**A root-scoped gate's count sits beside the components, in `root_findings`, and is not a sum over
them.** Semgrep runs once over the whole scan root, so its claims name no component, and nothing
invents one — which component contains a path is an ownership question the declaration does not
answer, and a claim outside every component would have nowhere to go. The projection carries it for
the same reason it carries the components.

**The channel is `scan.json`**, read out of each `--reports` directory beside the measurements;
`measurements.json` keeps its single writer. A recording workflow therefore runs a `scan` job
beside its measure matrix, with **no `--diff-base`** — on the default branch there is no change to
scope to, so the count is the repository's standing total — and `record` needs it while keeping
`if: !cancelled()`, because a red scan on the default branch is the most interesting thing a finding
history can hold and no later run can fill the hole. A measurements document is still required and a
scan document is not: only the first names the tree that binds the recording to the checkout. See
[ADR 0033](../../docs/adr/0033-a-finding-count-per-gate-reaches-the-ledger.md).

**No workflow in this repository currently runs `lydite test record` at all.** `lydite-baseline.yml`
was this repository's own recording workflow — `plan` → `measure` (the same matrix without
`--affected`, since ADR 0016 requires the default-branch run to be complete) → one `record` job, the
only job granted `contents: write` — and it no longer exists here: it was deleted rather than
rewired when this repository became a plain consumer of `lydite/actions` through `gt` (ADR 0051).
`lydite/actions`' own reusable `lydite-baseline.yml` exists and works, but nothing gt renders into
this repository's stage calls it yet (tracked as `pedromvgomes/gt#76`, external to this repository).
Pushes to the default branch here record no coverage baseline and append nothing to the ledger until
that route is restored. The description above of what a recording workflow does — the shard shape,
the `scan` job beside the measure matrix, the `mutate` job's `--no-gate` — is kept as reference
material for whatever calls `lydite test record` next, not as a description of a route running
today.

**The dashboard is not this slice.** `source/web/` is still empty; the hosted read path is
[ADR 0009](../../docs/adr/0009-quality-history-storage-and-access.md)'s later work.

**A finding's own detail reaches the branch too, as a transition and never as the full open
set.** Coverage, tests, CRAP and mutation stay scalars-only — that has not changed, and neither
has `Component.Findings`/`Record.RootFindings`, still a per-gate count. What is new is
`Record.FindingEvents`: `lydite test record` diffs the buckets a recording actually measured
against `ledger.OpenFindings`'s replay of the branch's own history and appends only the
fingerprints that newly appeared or resolved since that branch's last recording, never the
fingerprints still open and unchanged. A stable repository's ledger grows exactly as before;
growth is proportional to churn, not to the size of what is currently open, which is the same
reasoning ADR 0009 used to keep detail out of the ledger in the first place, applied one level
down instead of reopened. The cost this accepts: answering "is fingerprint X open right now"
needs a replay bounded by `lookbackMonths`, the same window `Latest` already bounds its own
back-walk by, rather than a single record read — `OpenFindings` is the one implementation of
that replay. See
[ADR 0058](../../docs/adr/0058-a-findings-detail-reaches-the-ledger-as-transitions.md).

A record's size is therefore no longer a single fixed number the way the paragraph above this
one might suggest for the scalar fields alone: a quiet commit — nothing appeared or resolved —
costs exactly what it always did, since `finding_events` is empty and omitted, while a commit
that introduces or clears findings costs more, in proportion to how many transitioned rather
than to how many are open.

