---
about: source/cli/internal/referral/disqualify.go
saw: feature/shell-is-scanned branch, review found the shellcheck gate had no disqualifier
---

Adding a new scanner (a new failing gate a component can be referred over) does not
automatically make `internal/referral`'s `Disqualifications` veto that scanner's own
suppression syntax — the ShellCheck scanner shipped, was reviewed, and only a follow-up
`deep`-panel review pass caught that no disqualifier covered `# shellcheck disable=...`
directives or a `.shellcheckrc`, meaning a change could clear a failing `shellcheck(<component>)`
row by suppressing the finding instead of fixing the script, then merge unread if it matched an
Exemption. Any new scanner (gosec, biome, a future language) needs the same check applied to it:
does the scanner have its own inline-suppression comment syntax or a config file that narrows
what it checks, and if so, is that syntax in `disqualify.go`'s `suppressionTokens` /
`substringSuppressionTokens` / a dedicated `contains<Tool>Directive` function, or is the
narrowing surface closed outright (as `--norc` closes ShellCheck's rc-file lookup in
`internal/shell/shell.go`'s `argv()`)? This is not something `internal/referral`'s existing
tests catch on their own — it has to be checked deliberately for each new scanner.
