---
name: no-lydite-annotation-excludes-a-scanner-finding
kind: rationale
description: The `[lydite:exclude_from_<gate>]` grammar is closed to mutation/crap/coverage on purpose — suppressing a gosec/semgrep/clippy finding is the tool's own inline annotation, not lydite's.
anchors:
  - path: source/cli/internal/annotation/*.go
    matches:
      - path: source/cli/internal/annotation/annotation.go
        blob: e77d0d7eba42
      - path: source/cli/internal/annotation/annotation_test.go
        blob: 65fe1b519e1e
  - path: .agents/references/configuration.md
    blob: 93bb994051d4
  - path: source/cli/internal/referral/disqualify.go
    blob: 0914611087d1
confidence: verified
---

The `Gate` enum (`internal/annotation/annotation.go:43`) has exactly three members —
`Mutation`, `CRAP`, `Coverage` (`annotation.go:45-55`) — and there is no fourth for a
generic scanner finding. `.agents/references/configuration.md:11-12` states the position
outright: `.lydite/config.yml` "does one thing, and tuning severity or suppressing
individual findings is not it (that's what a fix-up pass + inline `#nosec`/`nosemgrep`
annotations in the scanned repo are for)". So excluding a gosec/semgrep/clippy finding
today means writing **that tool's** suppression comment, and there is no lydite-native
form to add one with.

Why it is deliberate: a suppression must also refer the change. `suppressionTokens`
(`internal/referral/disqualify.go:41`) lists `nosemgrep`, `#nosec`, `//nolint`,
`#[allow(`, `#![allow(`, `biome-ignore`, `@ts-ignore`… — every one is an
evidence-based disqualifier read off the diff
(`docs/adr/0014-evidence-only-referral-matching.md:28`), so adding one does not silence
a finding for free, it costs unattended merge. ADR 0027 states the bargain for the
mutation annotation (`docs/adr/0027-mutation-is-its-own-command.md:141-148`): "the
annotation can add a referral but can never remove one".

**A new lydite gate would inherit that bargain automatically, not need wiring.**
`suppressionTokens` holds `annotation.Prefix` (`disqualify.go:51`, the shared
`"[lydite:exclude_from_"` at `annotation.go:68`) rather than the individual gate tokens,
explicitly so the list cannot go stale "the first time a gate is added, silently, in the
one place where a missed suppression means a change merges unread"
(`disqualify.go:43-50`). The obstacle to a scanner-exclusion annotation is therefore the
grammar and the scope resolver
([[exclusion-scope-is-a-funcdecl-or-a-mutant-span-never-a-symbol]]), not the referral
plumbing.

A related coarse escape was already rejected: a file-level or function-level opt-out for
mutation, in favour of the per-component `mutation: false` config key, because a broad
in-code escape duplicates a control that already exists and lives where its history is
the review record (`docs/adr/0027-mutation-is-its-own-command.md:150-153`).
