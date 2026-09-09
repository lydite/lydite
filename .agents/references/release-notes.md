# Release notes

> **The reference for `docs/release-notes/`.**

`release.yml` assembles the GitHub release body's header from
`docs/release-notes/_header.md` (the invariant install block — it lives there
rather than in `.goreleaser.yml`'s `header:` so the workflow can extend it) plus
`docs/release-notes/<tag>.md` when that file exists, passes it with
`--release-header`, and goreleaser appends its commit-derived changelog beneath.

A missing per-version file is deliberately not an error. Most patch releases are
fully described by their commit subjects, and failing a release over a file it
never needed would make every release depend on remembering a step — the same
failure the major-alias move in that workflow was automated away to prevent.
Write one when the release carries what a commit subject cannot: a change in
what an existing number or verdict *means*, an upgrade step consumers must take,
or a new major. `docs/release-notes/v0.2.0.md` is the worked example, and the
file has to be on `main` before the tag is pushed — the workflow reads it out of
the tagged tree.

