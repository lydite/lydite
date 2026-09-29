---
name: release-check-reads-to-head-not-the-tag
kind: gotcha
about: source/cli/internal/stages/release/commits.go
description: lydite release check's read-commits stage reads the commit range to HEAD, never to the tag string being checked, because gitstate.CommitMessages needs a resolvable git revision and the tag being released may not exist as a ref yet.
anchors:
  - path: source/cli/internal/stages/release/commits.go
    blob: 6c4c2d12f520933618cbbfcfca8088c568a5fae1
  - path: source/cli/internal/gitstate/gitstate.go
    blob: 1b2df596613f5a54dfc13fc50edb4e55957bba0a
confidence: verified
---

`gitstate.PreviousTag(ctx, dir, tag)` deliberately does not require `tag` to be a real git
ref — it is read purely as a semver string, so a release can be checked "from the commit
it is about to be cut at" before any tag object exists.

`releasestages.ReadCommits` (`internal/stages/release/commits.go`) has to honour that same
model for the commit range it reads: `gitstate.CommitMessages(ctx, in.Dir, in.Previous,
"HEAD")` bounds the range at `HEAD`, never at the tag string — `ReadCommitsIn` does not even
carry the tag. Passing the tag as the upper bound fails with a git error ("unknown
revision") whenever the tag is hypothetical — which is exactly the case a local or
pre-release check needs to support, and also happens to be harmless in the real
tag-triggered workflow: `release.yml`'s checkout is detached at the pushed tag when the
check runs, so `HEAD` and the tag's commit are the same commit there.

Anyone adding a second consumer of `PreviousTag` that also needs to read the commits in
the resulting range should read to `HEAD`, not to the version string, for the same reason.
