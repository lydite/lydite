---
about: a clearance job's checkout is the target repository itself — and two clearance stages disagree about which revision of it that should be; /lydite exempt reads exemptions off the working tree assuming the default branch, while a clearance's fingerprint is only taken when the checkout is the pull request's head
saw:
  - source/cli/internal/stages/clearance/clearance.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - source/cli/internal/reviewdecision/decide.go
  - source/cli/cmd/lydite/clearance.go
  - docs/adr/0015-clearance-binds-to-a-commit.md
---

Re-checked on `refactor/flow-architecture-clearance-pilot`, where `lydite clearance` became a
Flow. The checkout claim holds; the pointers moved and a second, conflicting assumption about
the same checkout is now visible side by side in one package.

**The checkout is the target repository.** A clearance workflow's `actions/checkout` with no
`repository:` override checks out the repository the pull request belongs to, not a separate
copy of lydite's source. ADR 0015's "the default branch's own copy of lydite" is easy to misread
as the latter. (lydite's own `.github/workflows/lydite-clearance.yml` was deleted 2026-09-22 by
ADR 0051's amendment; the mechanism applies identically to `lydite/actions`' reusable
`lydite-clearance.yml`.)

**`/lydite exempt` assumes that checkout is the default branch.** `clearancestages.uncoveredPaths`
(`internal/stages/clearance/clearance.go`) reads `.lydite/exemptions.yml` with a plain
`os.ReadFile(filepath.Join(dir, referral.FileName))`, and its doc says the working tree is "the
clearance job's own checkout of the default branch — so the declarations consulted are the ones
in force, never the ones the pull request proposes for itself". The changed paths come live from
`SCMRepository.ChangedPaths`, since no PR commits are in that tree. Contrast `review`, whose
checkout is the PR branch: `reviewdecision.exemptionsAt` (`internal/reviewdecision/decide.go`)
reads the file with `git show <base>:<path>` precisely so a PR cannot widen its own allowlist.

**`/lydite clear`'s fingerprint requires the checkout to be the PR head.** `clearedDecision`
(`internal/stages/clearance/fingerprint.go`) first runs `checkoutIsHead`, which refuses unless
`git rev-parse HEAD` in `dir` equals the live head. On a default-branch checkout — the ordinary
shape described above — the Fingerprint stage returns an empty fingerprint plus a warning
("this clearance records no fingerprint, so it will not carry onto a merge-queue entry"); the
clearance still resolves the referral on that head, it just does not carry onto a queue entry.

Consequence: one job's checkout cannot satisfy both at once. A job checked out at the PR head
gets a fingerprint, but `uncoveredPaths` then reads the exemptions file the pull request itself
proposes (only affecting the draft `/lydite exempt` proposes, which lands nothing). A job on the
default branch reads the exemptions in force but never fingerprints. Anyone changing which
revision a clearance job checks out, or migrating `uncoveredPaths` to read at a base, has to
decide this deliberately. The tension predates the Flow refactor (both functions existed in
`cmd/lydite/clearance.go` on `main`); the refactor only placed them in one package.
