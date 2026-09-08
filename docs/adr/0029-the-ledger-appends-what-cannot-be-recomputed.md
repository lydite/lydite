# The ledger appends what cannot be recomputed, and never lies about its gaps

[ADR 0009](0009-quality-history-storage-and-access.md) drew the distinction this record
implements: the per-commit coverage baseline is a **cache**, and quality history is a
**ledger**. This decides what that means in the code — where the append happens, how a gap
is recorded, what an entry is keyed by, and which scalars go in — and amends 0009 where
building it showed the design needed amending.

## One writer, one commit, two policies

The ledger lives on the same branch as the baselines and is landed by the same job, because
`lydite test record` is the only thing in either workflow holding a token that can push.
The append therefore rides inside the one commit `gitstate` already writes, and
`gitstate.WriteBaseline` becomes `gitstate.Write`, taking both.

A second *writer* is what the single-call-site invariant forbids, and this is not one. A
second *commit* was the alternative and was rejected for a reason beyond tidiness: it would
double the retry loop against a busy shared branch and leave a window in which the branch
holds a baseline with no record beside it — a hole in the history that lydite's own write
path invented, which the next append would then have to explain as a gap.

What the two policies do **not** share is when they are refused. A baseline is refused
whenever it would be partial, because any non-empty entry reads as a cache hit and a partial
one gates every later change on nothing. A record is appended whether or not what was
measured adds up to a baseline. The case that makes this concrete is a commit whose suite
went red: it establishes no baseline by construction, and its test counts — how many ran,
how many failed — are the data point a history most wants and the one nothing can recover
later.

So the staging is computed **inside** each push attempt rather than once ahead of the retry
loop. A baseline document is the same bytes whatever the branch holds; a record is appended
to the partition on the branch *as fetched*, and a retry fetches a branch a concurrent run
may have advanced.

## A gap is written by the next successful append, not by the failed one

ADR 0009 says a failed append is recorded as an explicit gap. Taken literally that is
impossible: the append that would have recorded it is the one that failed, so nothing wrote.
A sequence number consumed per append fails the same way — the number is consumed by the
write that did not happen, so the next append is contiguous and the hole is invisible.

The answer is two mechanisms, and the split is the decision:

- **Every record names its commit's first parent.** A reader that finds a record whose
  parent is not the previous record's commit has found a break using nothing but the file it
  is already reading. This cannot fail, because it is not written by the run that failed.
- **The next successful append writes an explicit `gap` record**, saying how many commits
  lie between the last recorded one and this one along the first-parent chain. That is the
  half a reader cannot work out alone, and it needs the repository — which the recording job
  has.

Rejected: a `tip` marker file naming the last recorded commit per branch. It is derivable
from the records, so it is state that can disagree with them, and the lookback that replaces
it (at most twelve months of partitions, read from a worktree already checked out) costs
nothing.

A width that cannot be established is recorded as unknown rather than guessed. A force-push,
an unrelated history and a checkout too shallow to see back all produce a real break of
unestablishable width, and an invented number would be worse than the honest absence. The
first record on a branch is never a gap: nothing precedes it, and claiming one would put a
break at the start of every line ever drawn.

## An entry is keyed by commit; a baseline is keyed by tree

They are different keys for different reasons, and copying one onto the other would be a
reflex rather than a decision.

A baseline answers *what was measured for this content*, so a pull request and the commit it
becomes deliberately share a key — that sharing is what lets a pull request's own
measurement become the baseline for the commit it lands as. History is a sequence of events;
two commits carrying the same tree are two points on the line, and collapsing them would
lose one. A record therefore carries the commit, its first parent, the branch, and the tree
— the tree so a record can be joined to the baseline recorded for the same content.

The timestamp is the **commit's own committer date**, never the clock at append time. A
partition is named from it, so a re-recording lands in the file the first attempt would have
used and the deduplication can find it there; and a CI job that ran hours late does not look
like a delayed commit.

A record's identity is its commit, its **branch** and its kind. A retried push, a re-run
workflow and a second `record` invocation all describe the same commit on the same branch,
and the append leaves one line. The branch is part of it because one commit is a point on
every branch that carries it: a release branch cut from the default one records it again,
and deduplicating across branches would leave that branch's history starting wherever it
next diverged — a hole nothing later fills, since every run would drop it the same way. The
kind is part of it because a gap always names the very commit whose entry sits beside it.

## Scalars per component, and the repository figure derived

ADR 0009 sized an entry at ~235 bytes, which is a repository-wide shape. This records them
**per component** instead, because the component is the unit every gate lydite has reports
at and a repository-wide number cannot answer which component moved. The repository figures
are sums over the components and are deliberately *not* stored beside them: two quantities
that must agree are two quantities free to disagree.

Every metric on a component is optional, and absent is not zero. A component with no
function above the CRAP threshold records zero and one lydite scores none of records
nothing; a series that read the second as the first would draw a clean line through a metric
nobody measured.

A ledger gains a field the way nothing else in lydite does. A baseline directory must be
versioned on a gained field, because an entry lacking it would still read as a cache hit —
that is the trap `crap/v1` exists as a separate directory to avoid. Nothing here is ever
compared against a stored record, so a field added later costs a series that starts on the
day it was added, which is what an append-only ledger is *for* and is rendered by the same
machinery that renders a gap.

## Which scalars, and which are not here yet

**In:** coverage counts, the two CRAP scalars, and test counts.

**Test counts are new work**, and are the reason `go-test`'s instrumented variant now runs
through a pinned **gotestsum**. `go test` writes no report of its own. lydite parsing `go
test -json` itself was the alternative and was rejected on the log rather than on the
counts: `-json` implies `-v`, so reconstructing the component's log from the event stream
turns one line per package into two lines per test, burying a failure under thousands of
passing tests in exactly the artifact a failing row points a reader at — and reconstructing
the non-verbose form instead means reimplementing gotestsum and being subtly wrong about it.
Its `pkgname` format is a better log than `go test` writes, and its JUnit is the format
cargo-nextest and vitest can both be made to write, so there is one reader rather than one
per runner. It is pinned, mirrored and Dependabot-visible per
[ADR 0006](0006-tool-pins-as-dependabot-manifests.md), and it is added to the
**instrumented** variant only — the plain one is what mutation runs once per mutant.

cargo-nextest gets its report through `--tool-config-file`, which inserts configuration
*below* the repository's own in priority. That is the opposite of the stance
[ADR 0008](0008-biome-as-the-only-typescript-linter.md) takes with Biome's `--config-path`,
and deliberately: a linter's rule set is lydite's verdict to fix, and a repository's test
configuration is the repository's. A repository that sets its own junit path therefore wins,
and that component contributes no counts — reported, not guessed at.

jest is the exception in the other direction. It bundles no JUnit reporter, and installing
`jest-junit` into the workspace would have lydite change what the repository resolves to,
which is the same rule that keeps lydite from installing a coverage provider.

**Mutation is out, for a structural reason rather than a scoping one.** Mutants come only
from lines the change touched, and on the default branch HEAD is its own merge-base — so the
post-merge recording job, the only one holding a token that can push, mutates nothing and
has nothing to record. Mutation results exist only on pull requests, in jobs deliberately
holding no token that can write. Recording them needs a route from a pull request's
measurement to the branch that does not hand a branch's own code a writable token, which is
[#49](https://github.com/lydite/lydite/issues/49) and is filed as
[#112](https://github.com/lydite/lydite/issues/112).

**Finding counts are out**, and are a slice of their own. `lydite scan` streams each tool's
own output live, because for a scanner the findings *are* the result; a count means each
scanner emitting a structured report and lydite rendering the findings from it, as it
already does for Biome alone. That is five parsers and a change to what reaches a terminal,
and it is the same work per-finding fingerprints need, which ADR 0009 already defers as
additive. Filed as [#111](https://github.com/lydite/lydite/issues/111).

## Partitions roll; the projection is per year

ADR 0009 requires every file to be independently fetchable under the GitHub Contents API's
1 MB cap, because the dashboard reads the branch directly with the viewer's own credentials
and has no other way in. The partition stays a month, and a **busier repository grows parts
within the month** — `2026-09.ndjson`, then `2026-09.1.ndjson` — rather than moving to a
finer granularity. A finer granularity would cost every reader more files forever to
accommodate a repository nobody has; a roll costs nothing until it fires. It happens
*before* the write that would cross the line, so no part is ever over the cap even
momentarily.

The projection is one file **per branch per calendar year**, holding one row per day: the
last record of that day, plus how many records and gaps the day held. Per branch, because a
day's row carries a scalar per component and a repository recording on several branches
would walk a shared file towards the same cap the partitions have a roll rule for — and
because history is per branch, so a chart reads exactly the branch it is drawing. Per year,
because that is what makes the bound hold by construction rather than by expectation. The
last record of the day and not a mean — a mean over a day is a number no commit ever had —
and the counts beside it are what make a downsampled row honest about how much it is
standing in for.

A branch name is a path segment there, which is safe by construction: git stores refs as
paths, so no branch name can be both a file and a directory in this tree any more than it
can be in `.git/refs`.

There is no index file. The directory listing is the index, which the Contents API already
serves, and an index is state that can disagree with the files it names.
