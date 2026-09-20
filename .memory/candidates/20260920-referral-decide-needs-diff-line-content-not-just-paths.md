---
about: internal/referral.Decide's Disqualifications computation reads actual added/removed diff-line text, not just a changed-path list — so it cannot be run from metadata alone
saw:
  - source/cli/internal/referral/changes.go
  - source/cli/internal/referral/disqualify.go
---

`referral.Change` (changes.go) carries `Added []DiffLine` and `Removed []DiffLine`, each
with a `Path` and the line's `Text` — not just a set of touched paths. `Disqualifications`
(disqualify.go, `for _, line := range ch.Added { ... containsAny(line.Text, ...) }`) scans
that line content for suppression tokens, skip tokens, etc. — a disqualifier veto is
fundamentally a content question, not a path question.

Consequence for anything trying to reuse `referral.Decide` or `Disqualifications` from a
context that only has a list of changed file names (e.g. a GitHub "list files changed" API
response, which gives filenames and diff stats but not full line-by-line added/removed
text without a further per-file patch fetch): it cannot compute Disqualifications at all,
and calling `Decide` with a `Change{Paths: ...}` that has empty `Added`/`Removed` would
silently report zero disqualifications rather than "unknown" — there is no signal
distinguishing "genuinely no disqualifying content" from "the caller never had the diff to
check." Anything that needs disqualifier-awareness from a metadata-only source has to
either fetch the real per-file patches (defeating the point of using metadata) or
explicitly document that it skips disqualifier detection.
