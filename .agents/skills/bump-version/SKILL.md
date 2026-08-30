---
name: bump-version
description: |
  Use this skill when the user asks to cut/release/bump a new lydite
  version (e.g. "cut a new version", "release with the recent fixes",
  "tag a release"). Covers picking the semver bump, tagging the release
  (which fires .github/workflows/release.yml → goreleaser), and verifying
  the published release.
---

# Cut a lydite release

Releases are **tag-driven**: pushing an annotated `vX.Y.Z` tag to a commit
on `main` triggers `.github/workflows/release.yml` (build & test →
goreleaser), which publishes the GitHub release with the `lydite`
binary (linux/darwin, amd64/arm64), `checksums.txt`, and `install.sh`.

Exactly one tag is involved, and you push it: the immutable `vX.Y.Z` release
tag. It is the only thing that triggers anything.

**This repository has no floating major-alias tag.** `install.sh` and
`lydite update` both resolve a version through the `releases/latest` redirect
or an exact `v<version>` tag, so nothing here ever reads a `vN`. The alias
belongs to `lydite/actions`, whose consumers pin `uses:
lydite/actions/scan@v1` — that repo's own `release.yml` moves it, and fails
the release if the remote did not actually advance.

So if you find yourself reaching for `git push --force origin v0` in this
repository, stop: there is nothing here that consumes it. Releasing a new
version of the action is a separate task in `lydite/actions`.

## 0. Preconditions

- The fixes to release are **already merged to `main`** and `main`'s CI is
  green. This skill does not merge PRs (see the repo CLAUDE.md rule). If the
  fix is still on a PR, stop and get it merged first.
- You are in the `gt` bare-repo layout; `gh`/`git` authenticate as the user
  in the root `.envrc` (`gh auth token --user <username>`). Read that user
  before any `gh`/push command.
- `git fetch origin --tags` so local tags and `origin/main` are current.

## 1. Pick the version

Find the current latest and what has landed since it:

```bash
gh release list --repo lydite/lydite --limit 5
git log --oneline "$(gh release view --repo lydite/lydite --json tagName -q .tagName)"..origin/main
```

Choose the bump by the **highest-impact change to the shipped binary**
since the last release (SemVer):

- **patch** (`vX.Y.Z+1`) — bug fixes only; no new user-facing behaviour.
- **minor** (`vX.Y+1.0`) — backward-compatible features / new flags / new
  scanner integrations.
- **major** (`vX+1.0.0`) — breaking changes (config schema changes, removed
  CLI surface, `lydite` baseline format changes). A new major also
  means a **new floating alias** (`vN+1`) and updating consumers that pin the
  old one — call this out explicitly.

Config-only changes (e.g. a `dependabot.yml` edit) do **not** by themselves
warrant a release; only cut one when the binary changed.

Let `$VER` be the new version (e.g. `v1.3.1`) and `$SHA` the `origin/main`
commit to release. `$MAJOR` is its alias (`v1`) — used only for verification
below, since the workflow derives and moves it itself.

## 2. Tag the release and push

Annotated tag (matches existing release tags), at the exact `main` commit:

```bash
git tag -a "$VER" "$SHA" -m "$VER

<one bullet per change being shipped, e.g. fix(...) (#NNN)>"
git push origin "$VER"
```

The push fires `release.yml`. Nothing else triggers it — only tags matching
`v*.*.*`.

## 3. The major alias moves itself

Nothing to do. `release.yml`'s final step derives `$MAJOR` from the tag you
pushed, force-moves it, and then re-reads it from the remote to confirm it
advanced — a push that silently no-ops fails the release rather than leaving
consumers pinned to the previous scanner.

Watch for it in the run; the job summary ends with `v1 now points at $VER`.

If that step fails, the release is **incomplete even though the assets
published**: `vX.Y.Z` exists and `vN` does not point at it, so no consumer
has the change. Fix the workflow and re-run the job rather than pushing the
alias by hand — a manual move hides the fact that the automation is broken,
and the next release breaks the same way.

## 4. Verify

```bash
gh run watch "$(gh run list --repo lydite/lydite --workflow release.yml \
  --limit 1 --json databaseId -q '.[0].databaseId')" \
  --repo lydite/lydite --exit-status
gh release view "$VER" --repo lydite/lydite \
  --json tagName,isDraft,isPrerelease,assets \
  -q '{tag:.tagName, draft:.isDraft, prerelease:.isPrerelease, assets:[.assets[].name]}'
git ls-remote origin "refs/tags/$MAJOR"   # must equal $SHA
```

The last line is the one that matters: it is the difference between a release
that shipped and a release that only looks like it did. `release.yml` already
asserts it, so a mismatch here means the workflow did not run its final step
at all.
