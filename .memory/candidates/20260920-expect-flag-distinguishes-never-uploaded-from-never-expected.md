---
about: lydite publish's buildComment cannot tell "artifact never uploaded" from "concern never expected" without --expect, because a directory that was never passed for a concern was previously indistinguishable from one that legitimately doesn't apply to this run
saw:
  - source/cli/cmd/lydite/publish.go
  - source/cli/cmd/lydite/publish_test.go
  - .github/workflows/lydite-pr.yml
---

Traced while fixing lydite/lydite#194 (referral vanished from the standing comment on four
merged PRs — #181, #182, #188, #193 — while the verdict still read "every check passed").

`buildComment(dirs, base, expect...)` only ever saw the directories `download-artifact` actually
found (globbed as `reports/*/` in the workflow). A concern whose artifact was never uploaded at
all — because the job died, or because the reports composite looked in the wrong directory
entirely (see the sibling gotcha about `lydite-reports`'s `path` input) — never produced an entry
in `dirs`. Before this fix there was no way for `buildComment` to tell that apart from a concern
this particular run legitimately never runs (e.g. `mutation` on a run that declined it): both
cases were "nothing in `dirs`", and both rendered as nothing at all — no section, no
`unmeasured`, no trace.

The fix is `--expect`: the caller (the workflow) now names the commands this run's own job graph
guarantees will produce a report, independently of what `download-artifact` found. A name in
`--expect` with no matching entry in `dirs` renders `unmeasured` (`absentConcern` in
`publish.go`); a name found in `dirs` is used normally; a name in neither list is simply never
mentioned, which is what lets a run that genuinely skips a concern stay silent about it. The
distinction is drawn entirely by the caller's `--expect` list — `buildComment` itself has no
way to know which concerns "should" exist for a given job, and encoding that anywhere inside
`publish.go` (e.g. hardcoding the four concern names) would tell every run it's missing
`mutation` even when a run legitimately never runs it.
