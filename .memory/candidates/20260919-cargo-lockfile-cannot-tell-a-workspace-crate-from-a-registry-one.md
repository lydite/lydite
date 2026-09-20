---
name: cargo-lockfile-cannot-tell-a-workspace-crate-from-a-registry-one
kind: gotcha
about: source/cli/internal/rust/lockfile.go
description: cargoLock's parser never records a [[package]] stanza's `source` key, so nothing distinguishes a workspace's own crate from a registry dependency — a comparison over Cargo.lock's full package set (e.g. a dependency-added check) will read a newly-declared local crate as an added third-party dependency.
anchors:
  - path: source/cli/internal/rust/lockfile.go
    blob: f568caf62f1d4d8f8c7b904622f84de197a15ca4
confidence: verified
---

`parseCargoLock` keeps only `name` and `version` per `[[package]]` stanza (as
`lines map[[2]string]int`), never the `source` key that would say whether a stanza is a
registry crate or a workspace member (a workspace member's stanza omits `source`
entirely; a registry one carries `source = "registry+..."`).

`internal/depdelta`'s `rust.LockDependencies` (added in the dependency-delta feature,
docs/adr/0047) derives its set from these same stanzas, so a `Cargo.lock` delta cannot
tell "a new third-party crate was pulled in" from "this repository declared a new crate
of its own" — both show up as an addition. npm's reader (`internal/typescript/licence.go`)
does make this distinction, by skipping the root entry and `link: true` workspace
members. This is an accepted, fail-safe gap (the wrong direction only ever produces an
extra referral, never a missed one) — fixing it needs `cargoLock` to also store each
stanza's `source` presence, which was deliberately left out of the additive read-only
exposure this feature added.
