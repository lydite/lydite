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
or an exact `v<version>` tag, so nothing here ever reads a `vN`, and
`release.yml` has no step that moves one. The alias belongs to
`lydite/actions`, whose consumers pin `uses: lydite/actions/scan@v1` — that
repo's own `release.yml` moves it, and fails the release if the remote did
not actually advance.

So if you find yourself reaching for `git push --force origin v0` in this
repository, stop: there is nothing here that consumes it, and nothing to
verify afterwards. Releasing a new version of the action is a separate task
in `lydite/actions`.

## 0. Preconditions

- The fixes to release are **already merged to `main`** and `main`'s CI is
  green. This skill does not merge PRs (see the repo CLAUDE.md rule). If the
  fix is still on a PR, stop and get it merged first.
- You are in the `gt` bare-repo layout; `gh`/`git` authenticate as the user
  in the root `.envrc` (`gh auth token --user <username>`). Read that user
  before any `gh`/push command.
- `git fetch origin --tags` so local tags and `origin/main` are current.
- **Dry-run goreleaser** from the tree you are about to tag:

  ```bash
  go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
  ```

  `release.yml` runs only on a `v*.*.*` tag push, so nothing in CI ever
  exercises `.goreleaser.yml`. A config error therefore surfaces for the
  first time *during* the release, once the tag is already public and
  immutable, and the recovery is a throwaway version number. The snapshot
  runs the same config through the same before-hooks, builds and archives,
  stopping short of publishing. Expect four binaries, four archives and a
  `checksums.txt` under `dist/`; that directory is gitignored, so delete it
  afterwards or leave it.

## 1. Pick the version

Find the current latest and what has landed since it:

```bash
gh release list --repo lydite/lydite --limit 5
git log --oneline "$(gh release view --repo lydite/lydite --json tagName -q .tagName)"..origin/main
```

The second command needs a release to exist; with none published yet, read
the whole history instead (`git log --oneline origin/main`).

Choose the bump by the **highest-impact change to the shipped binary**
since the last release (SemVer):

- **patch** (`vX.Y.Z+1`) — bug fixes only; no new user-facing behaviour.
- **minor** (`vX.Y+1.0`) — backward-compatible features / new flags / new
  scanner integrations.
- **major** (`vX+1.0.0`) — breaking changes (config schema changes, removed
  CLI surface, `lydite` baseline format changes).

Nothing pins a CLI version: the action's `version` input defaults to
`latest`, so **every release reaches every consumer on their next CI run**,
major ones included (ADR 0010). The bump number is a description of what
changed, not a gate on who receives it — which is what makes the release
notes below the actual mechanism for warning anyone.

Config-only changes (e.g. a `dependabot.yml` edit) do **not** by themselves
warrant a release; only cut one when the binary changed.

Let `$VER` be the new version (e.g. `v1.3.1`) and `$SHA` the `origin/main`
commit to release.

## 2. Write the release notes, if this release needs them

`release.yml` assembles the release body from `docs/release-notes/_header.md`
plus `docs/release-notes/$VER.md` when that file exists, and goreleaser
appends its commit-derived changelog underneath. A missing per-version file
is deliberately not an error — most patches are fully described by their
commit subjects.

Write one when the release carries what a commit subject cannot: a change in
what an existing number or verdict *means*, an upgrade step consumers must
take, or a new major.

**The file has to be merged to `main` before the tag is pushed.** The
workflow reads it out of the tagged tree, so a notes file written afterwards
is not in the release and cannot be got into it without a new version.

## 3. Tag the release and push

Annotated tag (matches existing release tags), at the exact `main` commit:

```bash
git tag -a "$VER" "$SHA" -m "$VER

<one bullet per change being shipped, e.g. fix(...) (#NNN)>"
git push origin "$VER"
```

The push fires `release.yml`. Nothing else triggers it — only tags matching
`v*.*.*`.

## 4. Verify

```bash
gh run watch "$(gh run list --repo lydite/lydite --workflow release.yml \
  --limit 1 --json databaseId -q '.[0].databaseId')" \
  --repo lydite/lydite --exit-status
gh release view "$VER" --repo lydite/lydite \
  --json tagName,isDraft,isPrerelease,assets \
  -q '{tag:.tagName, draft:.isDraft, prerelease:.isPrerelease, assets:[.assets[].name]}'
```

`assets` must list `install.sh` alongside the archives, the raw binaries and
`checksums.txt`. It is the one asset goreleaser does not build — it is copied
in via `extra_files` — and the install line in the README
(`releases/latest/download/install.sh`) resolves to it, so a release missing
it breaks new installs while looking complete.

Then check the body, if step 2 wrote a notes file:

```bash
gh release view "$VER" --repo lydite/lydite --json body -q .body | head -40
```

The notes must appear above goreleaser's generated changelog. A green run is
not evidence of this: the assembly step writes `release-header.md` and passes
it with `--release-header`, and if that step is absent or the file is not
found the release still publishes — with the header silently missing.
