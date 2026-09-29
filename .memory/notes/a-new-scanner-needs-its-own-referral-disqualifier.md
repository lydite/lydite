---
name: a-new-scanner-needs-its-own-referral-disqualifier
kind: gotcha
description: Adding a scanner does not make referral.Disqualifications veto that scanner's suppression syntax, so each new scanner must be checked for inline directives or config files that narrow it.
anchors:
  - path: source/cli/internal/referral/disqualify.go
    blob: 53409ea6d519
  - path: source/cli/internal/shell/shell.go
    blob: 614eb5669910
confidence: verified
---

Adding a scanner (a new failing gate a component can be referred over) does not make `internal/referral`'s `Disqualifications` veto that scanner's own suppression syntax. ShellCheck shipped, was reviewed, and only a follow-up `deep`-panel pass caught that nothing covered `# shellcheck disable=...` or a `.shellcheckrc`: a change could clear a failing `shellcheck(<component>)` row by suppressing the finding, then merge unread if it matched an Exemption.

For any new scanner (gosec, biome, a future language) ask: does it have inline-suppression comment syntax or a config file narrowing what it checks — and is that syntax in `disqualify.go`'s `suppressionTokens` (`:76`) / `substringSuppressionTokens` / a dedicated `contains<Tool>Directive`, or is the surface closed outright, as `--norc` closes ShellCheck's rc lookup in `internal/shell/shell.go`'s `argv()` (`:72`)? `internal/referral`'s existing tests do not catch this; it has to be checked deliberately per scanner.
