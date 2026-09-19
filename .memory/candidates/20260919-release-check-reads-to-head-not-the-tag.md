---
name: release-check-reads-to-head-not-the-tag
kind: gotcha
about: source/cli/cmd/lydite/release.go
description: lydite release check reads the commit range to HEAD, never to the tag string being checked, because gitstate.CommitMessages needs a resolvable git revision and the tag being released may not exist as a ref yet.
anchors:
  - path: source/cli/cmd/lydite/release.go
    blob: 0be59318699a2aff15d9aeb91ce1d9ba78effa49
  - path: source/cli/internal/gitstate/gitstate.go
    blob: be06ca1aa2328d8c112bf0e9f8acdf2a610eaf00
confidence: verified
---

`gitstate.PreviousTag(ctx, dir, tag)` deliberately does not require `tag` to be a real git
ref — it is read purely as a semver string, so a release can be checked "from the commit
it is about to be cut at" before any tag object exists.

`runReleaseCheck` (`release.go`) has to honour that same model for the commit range it
reads: `gitstate.CommitMessages(ctx, dir, previous, "HEAD")` bounds the range at `HEAD`,
never at the `tag` string. Passing `tag` itself as the upper bound fails with a git error
("unknown revision") whenever the tag is hypothetical — which is exactly the case a local
or pre-release check needs to support, and also happens to be harmless in the real
tag-triggered workflow: `release.yml`'s checkout is detached at the pushed tag when the
check runs, so `HEAD` and the tag's commit are the same commit there.

Anyone adding a second consumer of `PreviousTag` that also needs to read the commits in
the resulting range should read to `HEAD`, not to the version string, for the same reason.
