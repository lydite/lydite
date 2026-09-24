---
about: the licence dispatch switch in cmd/lydite/scan.go has no default case
saw:
  - source/cli/cmd/lydite/scan.go:200-207
---

The per-language `switch` that dispatches to a licence check after a component's other checks
(`scan.go:200-207`) has no `default` case. A language that passes `scannedLang` but has no case
in this switch produces no licence row at all — not an `unmeasured` row, an absent one, which
reads in the published document exactly like a gate that ran and found nothing. This is a
present defect, not hypothetical: it is harmless only because `scannedLang`'s enumeration and
this switch's case list happen to name the same three languages today. Surfaced while writing
ADR 0056 (`docs/adr/0056-a-component-states-its-language-only-where-no-runner-implies-one.md`),
which requires the switch's default become a panic (per
`agentic/rules/refuse-an-unhandled-grammar-rather-than-fall-through.md`) and an explicit
`unmeasured` case be added for any scanned language with no dependency set, as part of a future
implementing slice — not fixed by that ADR itself.
