---
about: internal/tsapisurface.Compare only installs and builds a tree that a compared package actually resolves a declaration in
saw:
  - source/cli/internal/tsapisurface/tsapisurface.go
---

`Compare` (source/cli/internal/tsapisurface/tsapisurface.go) runs `npm install`/`npm run
build` per tree through `needsTree(compared, at)` before reading either side — it does not
unconditionally prepare both `BaseDir` and `HeadDir`. This matters for a TypeScript
component a change introduces: its merge-base directory does not exist on disk at all, and
`surfaces()` already tolerates a base tree with no readable `package.json` (treating every
head declaration as an addition, per its own doc comment). Without the `needsTree` guard,
`prepare` would still try to `npm install` the nonexistent base directory, which fails and
turns every newly-introduced TypeScript component into an `Unmeasurable` referral instead
of a clean all-additions comparison — this was caught by `panel-code-review`'s correctness
pass on the introducing branch and fixed before merge. A regression test lives at
`TestReviewPassesATypeScriptComponentThisChangeIntroduces` in
`source/cli/cmd/lydite/review_test.go`.
