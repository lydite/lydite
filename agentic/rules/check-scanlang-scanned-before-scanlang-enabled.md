---
description: "Ask scanlang.Scanned before scanlang.Enabled, so a language lydite cannot scan never reads as switched off."
---

# Check `scanlang.Scanned` before `scanlang.Enabled`, never the reverse

`scanlang.Enabled` answers `false` for every language it has no key for — the same answer it
gives a language `.lydite/config.yml` switches off on purpose. Asking it before `Scanned` sends a
language lydite has no scanner for down the "switched off" branch, and reports "nothing scans
this" as an opt-out the repository never stated, rather than as a gap lydite itself has not
closed.

## Applies to

Any code that classifies a declared component's language before deciding whether its checks run
— today `PlanComponents` in `source/cli/internal/stages/scan/plan.go`.

## Example

```go
// wrong: a language scanlang has no scanner for reads as "the repository
// switched this off" instead of "lydite has no scanner for this"
if !scanlang.Enabled(lang, cfg) {
    entry.Disposition = Disabled
}

// right: ask whether lydite scans the language at all, first
if !scanlang.Scanned(lang) {
    entry.Disposition = Unscanned
} else if !scanlang.Enabled(lang, cfg) {
    entry.Disposition = Disabled
}
```

Reasoning: [ADR 0067](../../docs/adr/0067-scan-runs-as-eleven-single-responsibility-stages-walking-components-in-their-own-body.md)
and [`agentic/references/scanning.md`](../references/scanning.md).
