---
name: status-declined-is-not-status-context
kind: invariant
about: source/cli/cmd/lydite/publish.go
description: StatusDeclined and StatusContext are both non-voting and share the same default glyph, but worst()/counts()/headline() in publish.go treat them differently — a section made entirely of StatusDeclined rows is promoted out of StatusUnmeasured the way StatusPass is, while a section of only StatusContext rows is not.
anchors:
  - path: source/cli/cmd/lydite/publish.go
    blob: 5163b0502ce3af3cd421e8ae97f4fc7e7e8d334d
  - path: source/cli/internal/ui/row.go
    blob: a91e9da6e610aed1f40200e83e61091abfcc6424
confidence: verified
---

`worst(rows)` starts at `StatusUnmeasured` and is only promoted by `StatusFail` (returns
immediately), `StatusRefer`, `StatusPass`, and (as of ADR 0044) `StatusDeclined` — each of
the latter two only sets the accumulator when it is still `StatusUnmeasured`, so a real
`StatusFail`/`StatusRefer` elsewhere in the same section still outranks either. Crucially,
`StatusContext` rows are NOT in that promotion list at all: a section made entirely of
`StatusContext` rows (e.g. every component in it has `mutation: false`) stays
`StatusUnmeasured`, which is a known, deliberately unfixed gap (see ADR 0044's
"Consequences" section) — it is not the same bug `StatusDeclined` was added to fix, because
a per-component opt-out is a decision about one component inside a section that otherwise
ran, whereas `StatusDeclined` is for the whole concern never having been attempted at all
(the CLI command exiting immediately via a `--declined` flag before doing any component
work — see `cmd/lydite/mutation.go`'s `RunE`).

Anyone adding a new non-voting status to `internal/ui/row.go`'s closed set should decide
explicitly whether it needs the same promotion-out-of-unmeasured treatment `StatusDeclined`
and `StatusPass` get, or the "stays unmeasured" treatment `StatusContext` and `StatusNew`
get — the two are easy to conflate since both are non-voting for `verdictOf`.
