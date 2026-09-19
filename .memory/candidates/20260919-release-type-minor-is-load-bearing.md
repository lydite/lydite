---
about: cargo-semver-checks derives its own release type from the two Cargo.toml versions unless told otherwise, and that derivation is an author-controlled bypass
saw:
  - source/cli/internal/rustapisurface/rustapisurface.go
  - source/cli/internal/rustapisurface/testdata/version-bump-derived.stdout.txt
  - source/cli/internal/rustapisurface/testdata/version-bump-derived.stderr.txt
  - docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md
---

Measured directly: with no `--release-type` flag, `cargo-semver-checks` reads both
trees' `Cargo.toml` versions and only reports a lint whose severity is at or above what
that version delta permits. Bumping the head crate's version from `0.1.0` to `1.0.0` in
the same change that removes a public function turned a real `function_missing` break
into "no semver update required", exit 0 — a one-line edit to a file the author
controls, silently clearing the gate.

`internal/rustapisurface.argv` passes `--release-type minor` unconditionally, which is
what forces every `major`-severity lint to still be evaluated regardless of either
manifest's version. This is the entire compatible-change filter (the tool's own
catalogue types each of its 254 lints `major`/`minor`), so lydite maintains no lint
allow-list of its own. Any code that ever calls `cargo-semver-checks` (or a future
version bump of the pin) must keep this flag — deriving the release type is a bypass,
not a convenience, and the rule
`an-author-claim-may-only-add-a-referral-never-remove-one.md` names exactly why.
