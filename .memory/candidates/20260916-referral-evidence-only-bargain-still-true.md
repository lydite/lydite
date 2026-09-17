---
about: re-verified the evidence-only/claim-based disqualifier bargain and its ADR 0014/0027 pointers
saw:
  - source/cli/internal/referral/disqualify.go
  - source/cli/internal/annotation/annotation.go
  - docs/adr/0014-evidence-only-referral-matching.md
  - docs/adr/0027-mutation-is-its-own-command.md
  - agentic/references/configuration.md
targets: no-lydite-annotation-excludes-a-scanner-finding
verdict: still-true
---

Re-checked because the note was stale on four anchors.

All body line pointers are exact matches against current code:
`annotation.go:43` (`type Gate string`), `:51` (`CRAP Gate = "crap"`), `:68`
(`const Prefix = ...`); `disqualify.go:41` (`var suppressionTokens = ...`),
`:51` (`annotation.Prefix,`). The claim holds unchanged: `suppressionTokens`
still lists `annotation.Prefix` rather than per-gate tokens, ADR 0014's
"an author-controlled claim may only ever add a referral, never remove one"
still governs, and ADR 0027:141-148 still states the mutation-annotation
bargain in those terms.

The one drift: `.agents/references/configuration.md` (reported missing by
`agtk memory show`) has moved to `agentic/references/configuration.md`. The
quoted text at old `:11-12` is still present there, just re-numbered — did
not re-locate the exact new line range, not needed for this verdict.

Also relevant to ADR 0014, read directly (not itself memory content but
worth noting for future queries about the removed `!` conventional-commit
marker): ADR 0014 states the `!` marker was pulled because it is
claim-based — "nothing in lydite detects an undeclared API break, so
rewriting `feat!:` as `feat:` removes the disqualifier at no cost" — and
that it "returns alongside something that can catch its absence." At the
time this note was written, no note or candidate in this store discussed a
public-API-diff detector, issue #21/#23/#82/#24, or a joint design among
them. That gap is now closed: [ADR 0040](../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)
is the detector, built for Go only, with the `!` marker and the
`BREAKING CHANGE:`/`BREAKING-CHANGE:` footer read via `internal/declaration`
and honoured through two new `referral.Disqualification` kinds
(`disqualify.go`'s `DisqualificationAPIBreakDeclared`,
`DisqualificationAPISurfaceUncomputable`) that add a referral and never
touch `Disqualifications`'s evidence-only computation. No joint-design note
for #21/#23/#82 was ever found in this store, so that specific staged
candidate the handoff referenced does not exist — see
`agentic/references/referral-and-clearance.md`'s "A declared API break
refers, an undeclared one fails" section for the shipped design instead.
