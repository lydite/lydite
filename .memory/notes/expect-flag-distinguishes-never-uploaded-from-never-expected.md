---
name: expect-flag-distinguishes-never-uploaded-from-never-expected
kind: rationale
description: lydite publish's buildComment cannot tell "artifact never uploaded" from "concern never expected" from the directories it sees alone — --expect exists so the caller (the workflow) names which concerns this run's own job graph guarantees a report for, independently of what download-artifact found.
anchors:
  - path: source/cli/cmd/lydite/publish.go
    blob: e54b74595ac2
  - path: source/cli/cmd/lydite/publish_test.go
    blob: abfd6f83f95b
confidence: verified
---

Traced while fixing lydite/lydite#194 (a referral vanished from the standing comment on merged PRs
while the verdict still read "every check passed"). Before this fix, `buildComment(dirs, base,
expect...)` only ever saw directories `download-artifact` actually found. A concern whose artifact
was never uploaded at all (job died, or a reports composite looked in the wrong directory) never
produced an entry in `dirs` — indistinguishable from a concern this run legitimately never runs
(e.g. `mutation` on a run that declined it): both rendered as nothing at all, no section, no
`unmeasured`, no trace.

`--expect` (`publish.go`, `absentConcern` — confirmed at `publish.go:41`) names the commands this
run's own job graph guarantees will produce a report. A name in `--expect` with no matching entry in
`dirs` renders `unmeasured`; a name found in `dirs` is used normally; a name in neither list is never
mentioned, which is what lets a run that genuinely skips a concern stay silent about it. The
distinction is drawn entirely by the caller's `--expect` list — `buildComment` has no way to know
which concerns "should" exist for a given job, and hardcoding the four concern names inside
`publish.go` would tell every run it's missing `mutation` even when a run legitimately never runs it.
</content>
