# `lydite test record` writes the open-findings snapshot beside the ledger record

[ADR 0058](0058-a-findings-detail-reaches-the-ledger-as-transitions.md) puts a finding's detail on
the `lydite` branch as transitions: fingerprints that appeared or resolved, never the set still
open. That keeps the ledger proportional to churn, and it leaves the dashboard unable to answer
"what is open on this branch right now" without replaying the ledger, which carries fingerprints and
no location, rule or message. [ADR 0009](0009-quality-history-storage-and-access.md) has the
dashboard read the branch directly with the viewer's own credentials, so the answer has to be a file
that is fetchable under the Contents API's 1 MiB cap. This records that file, what it holds, and
what it deliberately does not.

[#345](https://github.com/lydite/lydite/issues/345) and
[#326](https://github.com/lydite/lydite/issues/326) proposed the shape. This amends both by dropping
`site`, for the reasons under Decision.

## Decision

### One file per branch, in parts that stay under the cap

`lydite test record` writes `snapshot/v1/<branch>/findings.json` on the `lydite` branch. A branch
name's slashes are real directories, and the branch is validated the way a ledger branch is. When
the document would exceed `maxPartitionBytes` (900 KiB, the headroom ledger partitions leave under
the 1 MiB cap) it is split into `findings.json`, `findings.1.json`, `findings.2.json`, and so on, so
no part is over that size.

Every part carries the same header, `{branch, commit, at, part, parts}`, so a reader can check the
set it fetched. A torn or mismatched set is **absent**: parts that disagree on `commit` or `parts`,
or a part the header says exists and the branch lacks. A reader never stitches a partial set
together.

### Each finding carries a location and a message, never source

A finding holds `gate`, `component`, `path`, `line`, `end_line`, `rule`, `severity`, `message`,
`ordinal` and `fingerprint`. A field with no value is omitted.

`site`, the excerpt of source a finding sits on, and `detail`, the tool's extended text, are
omitted. `detail` is dropped because tool text that quotes source is source: gosec's is the code
around the flagged line (a G101 hard-coded credential included) and clippy's is the rendered
diagnostic, which quotes the lines it points at. Dropping `site` is justified by two reasons:

1. A secret gitleaks flags would otherwise get a second copy in the `lydite` branch's history, one
   nobody scrubs when the source is. The branch is public for a public repository.
2. Excerpts are the bulk of a finding's size and push the file towards the Contents API's cap.

The dashboard fetches the excerpt from the code at `path` and `line`, and the extended text with it. `end_line` is
position, not source, and stays. `ordinal` stays because it tells two identical claims
in one file apart.

### Membership is the ledger's open set; the previous snapshot supplies fields only

The set of entries is exactly the open set `ledger.BranchState` replays after this commit's own
events have been applied. For each open fingerprint:

- a bucket (gate and component) this run measured takes its fields from this run's findings;
- a bucket this run did not measure, because its scanner crashed or did not run, takes its fields
  from the previous snapshot where that holds the fingerprint (a carry-forward);
- when neither has it, the entry is identity-only: `fingerprint`, `gate`, `component`.

Membership must not be read off the previous snapshot. A snapshot that lags the ledger (a failed or
torn write, a run that landed no part) would carry findings the ledger has resolved forward and drop
ones it has opened. The ledger is the authority on what is open; the snapshot is only a place to
find the words for it.

### Written after the append, in the same commit

The parts are staged after `ledger.Append`, inside the same `gitstate.Write` commit, so the snapshot
and the record that explains it land together or not at all. When `Append` landed nothing, as in a
run for a SHA already recorded, the snapshot is not written: it would describe an open set the ledger
has not moved to.

### A Cache, not a Ledger

The snapshot is fully recomputable from the ledger plus a rerun, so it is a Cache in the sense of
the [state-consulted-to-skip-work rule](../../agentic/rules/a-state-consulted-to-skip-work-is-a-cache-never-a-ledger.md). Losing it or
tearing it costs fields, never history; the next recording rewrites it whole.

Inside `gitstate` the type is the open-findings snapshot, `OpenFindings`, never a bare "snapshot":
`gitstate.Snapshot` already names the coverage and CRAP baseline.

## Rejected

**Keeping `site`.** It answers "what does the code say" without a second fetch, and costs a copy of
any secret in the branch's history plus most of the file's size.

**Keeping `detail`.** It spares the dashboard a fetch for the extended text, and copies quoted
source, secrets included, into the branch's history exactly as `site` would.

**Taking membership from the previous snapshot and patching it with events.** It is smaller work
and wrong whenever the snapshot lags, as above.

**Writing the snapshot as a second commit.** It doubles the retry loop against a busy branch and
leaves a window where the ledger has a record with no snapshot beside it, the reasoning
[`quality-history.md`](../../agentic/references/quality-history.md) gives for one writer and one
commit.

**Putting the open set in the ledger record.** It makes a record's size proportional to what is
open instead of to churn, which is what ADR 0058 avoided.

## Consequences

- The dashboard reads one small set of files per branch for the open findings, and the ledger for
  their history.
- A bucket whose scanner keeps crashing keeps its entries but they go stale in their fields; a
  newly opened finding there is identity-only until a clean run.
- A branch with a very large open set grows more parts, each independently fetchable.
- Source text never reaches the snapshot, so scrubbing a leaked secret from the source removes it
  from everything lydite publishes going forward.
