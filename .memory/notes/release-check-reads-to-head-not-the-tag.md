---
name: release-check-reads-to-head-not-the-tag
kind: gotcha
description: lydite release check reads the commit range to HEAD, never to the tag string being checked, because gitstate.CommitMessages needs a resolvable git revision and the tag being released may not exist as a ref yet.
anchors:
  - path: source/cli/cmd/lydite/release.go
    blob: 74147cd9c206
  - path: source/cli/internal/gitstate/gitstate.go
    blob: 1b2df596613f
confidence: suspect
---

`gitstate.PreviousTag(ctx, dir, tag)` deliberately does not require `tag` to be a real git ref — it
is read purely as a semver string, so a release can be checked "from the commit it is about to be
cut at" before any tag object exists.

`runReleaseCheck` has to honour that same model for the commit range it reads:
`gitstate.CommitMessages(ctx, dir, previous, "HEAD")` bounds the range at `HEAD`, never at the
`tag` string. Passing `tag` itself as the upper bound fails with a git error ("unknown revision")
whenever the tag is hypothetical — exactly the case a local or pre-release check needs to support,
and harmless in the real tag-triggered workflow, whose checkout is detached at the pushed tag when
the check runs (`HEAD` and the tag's commit are the same commit there). Anyone adding a second
consumer of `PreviousTag` that also needs to read the resulting commit range should read to `HEAD`,
not to the version string.
</content>
