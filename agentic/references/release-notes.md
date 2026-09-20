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

`lydite release check`, the step immediately before this assembly in
`release.yml`, is an unrelated gate over an unrelated question. This file is
about what a human reads describing the release; that check is about whether
the *version number* being tagged is allowed to carry a declared break, and it
can fail the release outright, stopping it before goreleaser runs — nothing
here ever does. Per [ADR 0045](../../docs/adr/0045-a-tag-that-is-not-the-breaking-bump-cannot-carry-a-declared-break.md)'s
own "Release notes are out of scope" section, the check does not look at
`docs/release-notes/<tag>.md` at all: a missing per-version file is not a
release-time condition for it, any more than a declared break is a condition
here.

