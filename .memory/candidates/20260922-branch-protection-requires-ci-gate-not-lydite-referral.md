---
about: this repository's actual required branch-protection check is ci-gate (from gt's own pipeline), not lydite/referral — lydite's own CI has never been what merge is gated on here
saw:
  - .gt-repo.yaml
  - lydite/lydite#34
---

`.gt-repo.yaml`'s own header comment states it plainly: "Branch protection needs exactly one
check, ci-gate, which waits on all of [the pipeline stages gt renders into
`ci-orchestration.yml`]." `lydite/referral` and `lydite/clearance` are informational statuses
lydite posts, not required checks a ruleset enforces here — confirmed independently by
`lydite/lydite#34` ("The referral status enforces nothing until it can be a required check"),
still open.

This is what made deleting `.github/workflows/lydite-pr.yml`, `lydite-baseline.yml` and
`lydite-clearance.yml` outright (ADR 0051's 2026-09-22 amendment) safe to do without breaking
merges: nothing in this repository's own branch protection depended on those files' statuses
existing. A repository that *has* made `lydite/referral` a required check (a real consumer,
once one exists) would not have this same freedom — deleting its gate wholesale would leave
every pull request permanently unmergeable rather than merging unreviewed.
