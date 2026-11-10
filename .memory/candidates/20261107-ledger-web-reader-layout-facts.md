---
about: "ledger layout facts a non-Go reader needs: the open-findings snapshot under snapshot/v1, rollup drops finding_events, 12-month window is partition-calendar-month based, skip-unparseable-line rule"
saw:
  - source/cli/internal/ledger/ledger.go
  - source/cli/internal/ledger/open.go
  - source/cli/internal/gitstate/gitstate.go
  - source/cli/internal/stages/ledger/compose.go
---
Found while scoping a browser ledger reader (issue #331).

- An open-findings snapshot is written beside the ledger: `snapshot/v1/<branch>/findings.json` and its parts (internal/ledger/open.go:65), staged by `gitstate.stageOpenFindings` (gitstate.go:964) in the same commit as the record, only when `ledger.Append` landed one. `gitstate.Snapshot` still holds only the Coverage and CRAP baselines under `v4/<key>.json`. Otherwise the findings detail on the branch is `Record.FindingEvents` transitions (ledger.go:199).
- `Rollup` (ledger.go:713-730) carries Components and RootFindings but NOT finding_events, so the open set can only be rebuilt from monthly partitions, never from `daily/`.
- `BranchState` window (ledger.go:532-549): walks `lookbackMonths`=12 partition months starting at the month of `before` (inclusive), stepping back one calendar month (anchored to day 1). Not a rolling 365 days. Replay uses `rec.At.Before(before)` (exclusive), sorts stably by At, KindEntry + matching Branch only; resolved on an empty bucket deletes the bucket.
- A resolved event carries no path/rule (compose.go findingEvents comment); only the appearance does.
- `scan` (ledger.go:791-821) silently skips any line that fails json.Unmarshal, including a line whose `at` is not a valid RFC3339 time; a reader that throws on one bad line diverges from Go.
- No testdata ledger fixture exists (find for *history*/ndjson under testdata finds only scanner outputs); ledger_test.go builds records inline.
