---
about: referral.Decide only populates Uncovered when no exemption covers the change; a referral caused purely by a disqualifier leaves Uncovered empty
saw:
  - source/cli/internal/referral/decide.go
---

Read while researching ADR 0053 (clearance survives the merge queue), which proposes
fingerprinting `referral.Uncovered(paths, exemptions)` as the identity of what a clearance
covers.

`Decide` (`source/cli/internal/referral/decide.go:66`) has two branches that reach
`Referred = true`:

1. No single exemption covers every changed path: `d.Uncovered = Uncovered(ch.Paths,
   file.Exemptions)` is populated with the paths nothing covers (`decide.go:97`).
2. An exemption *does* cover every path, but `Disqualifications` vetoed the match
   (`d.Referred = len(d.Disqualifications) > 0`, `decide.go:100`) — in this branch
   `d.Uncovered` is never assigned, so it stays nil/empty.

So `Uncovered` is `[]` both for a change that is fully exempt (fine) and for a change that is
exempt-by-path-coverage but referred anyway because of a disqualifier (net-new `#nosec`, a
skipped test, an edit to `.github/workflows/`, an exemptions-file edit, a declared API break,
etc.). Any scheme that fingerprints only `Uncovered` treats every disqualifier-only referral as
the same identity ("empty set"), regardless of which disqualifier fired or whether it's even
present on the rebased/re-queued tree. Confirmed by reading `Decide` and `Uncovered` directly;
`Uncovered`'s own doc comment (`decide.go:150`) already says this is "a weaker test than the
all-or-nothing rule `Decide` applies" and is deliberately not the verdict, but that a
disqualifier-only referral gives literally no signal at all in the value (as opposed to a
partial one) is easy to miss.
