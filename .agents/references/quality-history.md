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
component moved. Every metric is a pointer, so absent and zero are different answers: a
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

**Coverage, CRAP and test counts are in; mutation and finding counts are not, for different
reasons.** Mutation is structural: mutants come only from lines the change touched, and on
the default branch HEAD is its own merge-base, so the one job holding a token that can push
mutates nothing. Its results exist only on pull requests, in jobs deliberately holding no
writable token
([#112](https://github.com/lydite/lydite/issues/112), and [#49](https://github.com/lydite/lydite/issues/49)
behind it). Finding counts wait on the parsers rather than on the channel: findings travel as
data (see [findings.md](findings.md)), and Biome is the only scanner whose report lydite reads —
`lydite scan` streams the other seven because for a scanner the findings *are* the result, so
a count over a component means every one of them emitting a structured report lydite renders
the findings from ([#111](https://github.com/lydite/lydite/issues/111)).

**The dashboard is not this slice.** `source/web/` is still empty, the hosted read path is
[ADR 0009](../../docs/adr/0009-quality-history-storage-and-access.md)'s later work, and per-finding
fingerprints are additive, and cheap: every finding already carries the identity they would be
keyed by.

