---
about: the scan's licence dispatch (licenceGateFor) names every scanned language and refuses the rest; a scanned language with no dependency set gets an explicit not-gated verdict that renders as an unmeasured row, never an absent one
saw:
  - source/cli/internal/stages/scan/licences.go
  - source/cli/cmd/lydite/scan.go
  - docs/adr/0056-a-component-states-its-language-only-where-no-runner-implies-one.md
---

The per-language licence dispatch is `licenceGateFor` in `internal/stages/scan/licences.go`. It
names Go (`goLicence`), Rust (`rustLicence`), TypeScript (`typescriptLicence`) and Shell
(`noLicenceGate`), and its `default` returns an error ("is planned for scanning and has no licence
gate"). `gateLicences` calls it for every `Scan` plan entry before creating the merge-base
worktree, so an unhandled language stops the scan before anything is read or checked out, rather
than producing no licence row — the fall-through ADR 0056 required be closed, per
`agentic/rules/refuse-an-unhandled-grammar-rather-than-fall-through.md`.

A scanned language with no dependency set is handled explicitly, not by omission: `noLicenceGate`
returns the zero `LicenceVerdict`, whose `Gated` is false, and `recordLicence` (`cmd/lydite/scan.go`)
renders `!v.Gated` as an `unmeasured` `licence(<name>)` row ("<lang> declares no dependency set to
read licences from"). A new scanned language therefore needs a `licenceGateFor` case of its own —
either a real gate or `noLicenceGate` — and gets a refusal, not silence, if it has neither.
`recordComponents` in the same file likewise refuses a plan `Disposition` it has no rows for
rather than rendering nothing.
