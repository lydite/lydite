---
name: referral-disqualifications-read-diff-line-content
kind: gotcha
description: referral.Disqualifications scans added and removed diff-line text, so a caller holding only changed paths would silently report no disqualifications rather than unknown.
anchors:
  - path: source/cli/internal/referral/changes.go
    blob: 6488d7d4e36f
  - path: source/cli/internal/referral/disqualify.go
    blob: 53409ea6d519
confidence: verified
---

`referral.Change` (`internal/referral/changes.go:44-48`) carries `Added []DiffLine` and `Removed []DiffLine`, each with a `Path` and the line's `Text` — not just touched paths. `Disqualifications` (`disqualify.go`, e.g. `containsAny(line.Text, suppressionTokens)` near line 220) scans that content for suppression and skip tokens; a veto is a content question, not a path question.

So a caller with only a changed-file list (a GitHub "list files" response has names and stats, not line text) cannot compute disqualifications. Calling `Decide` with a `Change{Paths: ...}` and empty `Added`/`Removed` reports zero disqualifications — indistinguishable from "genuinely nothing disqualifying" — rather than "unknown". Such a caller must fetch per-file patches or document that it skips disqualifier detection.
