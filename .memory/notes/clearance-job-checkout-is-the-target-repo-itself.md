---
name: clearance-job-checkout-is-the-target-repo-itself
kind: gotcha
description: A clearance job's checkout is the target repository itself, and two clearance code paths disagree about which revision of it that should be — /lydite exempt reads exemptions off the working tree assuming the default branch, while a clearance's fingerprint is taken only when the checkout is the pull request's head.
anchors:
  - path: source/cli/internal/stages/clearance/clearance.go
    blob: 30f5f38d1f2d
  - path: source/cli/internal/stages/clearance/fingerprint.go
    blob: 516836b3bf3a
  - path: source/cli/internal/reviewdecision/decide.go
    blob: 8dcf55653824
  - path: docs/adr/0015-clearance-binds-to-a-commit.md
    blob: 3af3cac1bd4d
confidence: verified
---

A clearance workflow's `actions/checkout` with no `repository:` override checks out the repository
the pull request belongs to, not a separate copy of lydite's source. ADR 0015's "the default
branch's own copy of lydite" is easy to misread as the latter.

**`/lydite exempt` assumes that checkout is the default branch.** `uncoveredPaths`
(`clearance.go:154`) reads `.lydite/exemptions.yml` with a plain `os.ReadFile`, and its doc says the
working tree is "the clearance job's own checkout of the default branch — so the declarations
consulted are the ones in force, never the ones the pull request proposes for itself". Contrast
`review`, whose checkout is the PR branch: `reviewdecision.exemptionsAt` reads the file with `git
show <base>:<path>` precisely so a PR cannot widen its own allowlist.

**`/lydite clear`'s fingerprint requires the checkout to be the PR head.** `clearedDecision`
(`fingerprint.go:96`) first runs `checkoutIsHead` (`fingerprint.go:158`), which refuses unless `git
rev-parse HEAD` in `dir` equals the live head. On a default-branch checkout the Fingerprint stage
returns an empty fingerprint plus a warning ("this clearance records no fingerprint, so it will not
carry onto a merge-queue entry"); the clearance still resolves the referral, it just does not carry
onto a queue entry.

Consequence: one job's checkout cannot satisfy both at once. A job checked out at the PR head gets a
fingerprint, but `uncoveredPaths` then reads the exemptions file the pull request itself proposes. A
job on the default branch reads the exemptions in force but never fingerprints. Anyone changing
which revision a clearance job checks out, or migrating `uncoveredPaths` to read at a base, has to
decide this deliberately.
</content>
