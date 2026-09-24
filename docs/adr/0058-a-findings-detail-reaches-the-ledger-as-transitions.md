# A finding's detail reaches the ledger as appearance and resolution transitions, never the full open set

[#241](https://github.com/lydite/lydite/issues/241) names the gap: the ledger records how many
claims each gate made (`Component.Findings map[string]int`, `Record.RootFindings`, both from #131)
and nothing about which ones. "When did this finding first appear?" is unanswerable, and a
located finding's only durable home today is a pull-request thread that scrolls away with the
pull request — pre-existing debt anchored off any changed line never even gets that far.

[ADR 0009](0009-quality-history-storage-and-access.md) already drew the line this record extends
rather than reopens: only scalars belong in the ledger, and it named the reason — a 150-finding
list is ~24 KB against ~5.5 MB for five years of scalar history, and keeping detail out is what
lets history be retained forever, since nothing here is ever pruned (`ledger.go:62-74`,
`appendLine`'s `O_APPEND`-only open at `ledger.go:696`). ADR 0009 anticipated exactly this slice:
*"adding stable per-finding fingerprints later is additive."* This record is that addition, and
needs no `history/v1` bump — the version exists for a change that makes an *old* record
unreadable or mean something else (`ledger.go:62-74`), and every reader here still parses a record
missing the new field as one with no finding events, which is honestly what it has.

## The identity is `v1:<16 hex>`, not `lydite:finding:v1:<hash>`

The issue writes the fingerprint as `lydite:finding:v1:<hash>`. The code does not:
`Finding.Fingerprint()` (`internal/finding/finding.go:161-176`) returns
`Version + ":" + hex[:16]` — `v1:<16 hex characters>`. `lydite:finding:` is the thread marker's
own prefix in `internal/threads`, a different string for a different purpose. The ledger stores
the fingerprint exactly as `Fingerprint()` returns it.

Its ingredients (`Gate`, `Component`, `Path`, `Site`, `Ordinal` — deliberately never `Line`,
`Message` or `Severity`) are what make it survive a file moving around or a tool rewording its
own diagnostic, and are the property this whole design rests on: a fingerprint that outlives
cosmetic churn is one worth tracking appearance and resolution of at all.

## Recorded as transitions, not as the full open set on every recording

The issue names the real design question: re-record the whole open set every time, or record
only what changed. Full-set-every-time is easy to read a point-in-time snapshot back from, but it
re-pays the same finding's cost on every recording for as long as it stays open — which,
for pre-existing debt, is indefinitely. A repository with 30 persistent findings would write
those 30 identities into every future recording, forever, which is the exact bloat ADR 0009 kept
out of the ledger by choosing scalars, reintroduced one level down.

**A recording appends only the fingerprints that newly appeared and the ones that resolved since
the branch's last recording.** A stable repository — the common case — adds nothing beyond its
existing scalars on most commits. Growth is proportional to churn, not to the size of the open
set. "When did this first appear" is answered by one line instead of a scan for the earliest
recording that happens to list it.

The cost this accepts, stated rather than hidden: reconstructing the open set at an arbitrary
point in time means replaying transitions rather than reading one recording. That replay is
bounded the same way `Latest` already bounds its own back-walk — `lookbackMonths` (12) — and for
the same reason: a branch with nothing in that window is adopting detail, not resuming a history
worth reconstructing further back.

## What a finding event carries

`Fingerprint`, `Gate`, `Component` (structural — a transition without them cannot be bucketed),
plus `Path` and `Rule`. Enough to render `gosec(cli): G101 in internal/foo.go` in a history view
without a second lookup.

**`Message` is left out.** A tool that rewords its own diagnostic has not found something
different — the same reasoning `Fingerprint` itself already applies to exclude `Message` from the
hash. Storing it frozen at first-appearance time risks a rendered history showing wording a
current run of the same tool would no longer produce for the same rule, next to a fingerprint
that correctly says nothing about the claim changed. `Rule` identifies what fired; `Path` says
where; neither drifts the way a message's prose can.

## Shape

```go
// FindingTransition is what happened to a fingerprint between the branch's
// last recording and this one.
type FindingTransition string

const (
	// FindingAppeared is a fingerprint this recording's scan holds that the
	// branch's open set, replayed up to this commit, did not.
	FindingAppeared FindingTransition = "appeared"
	// FindingResolved is a fingerprint the branch's open set held that this
	// recording's scan, for the same gate and component, does not.
	FindingResolved FindingTransition = "resolved"
)

// FindingEvent is one fingerprint's transition, carried on the record it
// transitioned on.
type FindingEvent struct {
	Transition  FindingTransition `json:"transition"`
	Fingerprint string            `json:"fingerprint"`
	Gate        string            `json:"gate"`
	Component   string            `json:"component,omitempty"`
	Path        string            `json:"path"`
	Rule        string            `json:"rule,omitempty"`
}
```

`Record` gains `FindingEvents []FindingEvent \`json:"finding_events,omitempty"\``. Not named
`Findings`, which `Component` already uses for the per-gate scalar counts this is additive to —
one word must not answer two different questions on two types in the same package.

A **bucket** is `(Gate, Component)` — `Component` empty for a root-scoped gate, exactly as
`RootFindings` already treats it. Resolution is scoped to buckets this recording actually
measured: a finding belongs to a bucket that did not run this time (a component #253's scanner
work has not yet touched, a gate switched off, a partial recording) is left alone rather than
marked resolved, mirroring `findingCounts`'s own existing rule that an inapplicable gate is not a
key at all. The candidate buckets are exactly the keys already present in `findingCounts`'s
`perComponent` and `root` maps — the recorder does not need a second notion of "what ran".

Reconstructing the open set: `ledger.OpenFindings(root, branch string, before time.Time) map[FindingBucket]map[string]bool`
(new; `FindingBucket{Gate, Component}` new) replays every `KindEntry` record's `FindingEvents` for
`branch`, across the same `lookbackMonths` window and partition walk `Latest` already performs,
sorted ascending by the record's own `At` (not file order — `Latest`'s own doc comment already
notes recordings can land out of order) and applies `FindingAppeared`/`FindingResolved` in that
order. Bounded, and no new I/O pattern: it is the same walk `Latest` already pays for, reading the
same partitions, once per recording.

## Sizing

`ledger.go:100-110`'s "roughly 300 bytes a record, a month holds around 3,000 recordings" describes
the scalar-only record and must move with this change, but not into a fixed number: a record's
size is no longer solely a function of component count. A quiet commit — no finding appeared or
resolved anywhere — costs exactly what it costs today, since `finding_events` is empty and
omitted. A commit that introduces or clears findings adds roughly 100-150 bytes per transition
(a 19-character fingerprint, a gate and rule name, a path). The comment moves to state the
dependency plainly: unchanged findings are free; new or resolved ones cost proportionally to how
many transitioned, not to how many are open. `maxPartitionBytes`'s roll behavior (`ledger.go:110`)
already handles a partition that grows past what a quiet month would predict — that is what
rolling to a numbered sibling is for.

## What this does not do

No SARIF export — out of scope per the issue, and opt-in/default-off whenever it lands, since
code scanning's dashboard needs GitHub Advanced Security and lydite exists partly to give a
private repository without it the checks it otherwise cannot have.

No dashboard reads this yet — `source/web/` is still empty. This slice's payoff is that history
recorded from the day this lands is complete once the dashboard exists to read it; ADR 0009's own
reasoning already covers why that is worth having before the reader does.

## Consequences

- A stable repository's ledger grows exactly as it does today. An active one's grows in
  proportion to finding churn, never to the size of what is currently open.
- Reading "is fingerprint X currently open" needs a replay bounded by `lookbackMonths`, not a
  single record read. `OpenFindings` is the one implementation of that replay; nothing may
  reimplement it.
- A recording that ran a subset of gates or components — partial, or ahead of a scanner landing —
  never manufactures a resolution for a bucket it did not measure.
- `Message` is not in the ledger. A reader wanting the claim's exact wording at the time it was
  open has it nowhere durable — the same gap the issue's own SARIF follow-on exists to eventually
  close for platforms that want it.
