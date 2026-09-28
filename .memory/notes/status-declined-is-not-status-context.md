---
name: status-declined-is-not-status-context
kind: invariant
description: StatusDeclined and StatusContext are both non-voting and share the same default glyph, but worst()/counts()/headline() in publish.go treat them differently — a section made entirely of StatusDeclined rows is promoted out of StatusUnmeasured the way StatusPass is, while a section of only StatusContext rows is not.
anchors:
  - path: source/cli/cmd/lydite/publish.go
    blob: e54b74595ac2
  - path: source/cli/internal/ui/row.go
    blob: a91e9da6e610
confidence: suspect
---

`worst(rows)` starts at `StatusUnmeasured` and is only promoted by `StatusFail` (returns
immediately), `StatusRefer`, `StatusPass`, and (ADR 0044) `StatusDeclined` — each of the latter two
only sets the accumulator when it is still `StatusUnmeasured`, so a real `StatusFail`/`StatusRefer`
elsewhere in the same section still outranks either. `StatusContext` rows are NOT in that promotion
list: a section made entirely of `StatusContext` rows (e.g. every component has `mutation: false`)
stays `StatusUnmeasured` — a known, deliberately unfixed gap (ADR 0044's "Consequences"). It is not
the same bug `StatusDeclined` fixes: a per-component opt-out is a decision about one component
inside a section that otherwise ran, whereas `StatusDeclined` is for the whole concern never having
been attempted (the CLI exiting immediately via a `--declined` flag — see
[[declined-flag-short-circuits-before-component-load]]).

Anyone adding a new non-voting status to `internal/ui/row.go`'s closed set should decide explicitly
whether it needs the same promotion-out-of-unmeasured treatment `StatusDeclined`/`StatusPass` get,
or the "stays unmeasured" treatment `StatusContext`/`StatusNew` get — easy to conflate since both
are non-voting for `verdictOf`.
</content>
