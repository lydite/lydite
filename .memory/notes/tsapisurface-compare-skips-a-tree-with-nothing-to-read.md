---
name: tsapisurface-compare-skips-a-tree-with-nothing-to-read
kind: gotcha
description: tsapisurface.Compare installs and builds a tree only when a compared package resolves a declaration in it, so a newly introduced TypeScript component's absent base tree is not an unmeasurable referral.
anchors:
  - path: source/cli/internal/tsapisurface/tsapisurface.go
    blob: 97cf980e6f5d
confidence: verified
---

`Compare` (`internal/tsapisurface/tsapisurface.go`) runs `npm install`/`npm run build` per tree through `needsTree(compared, at)` (call at line 181, definition at 247) before reading either side; it does not unconditionally prepare both `BaseDir` and `HeadDir`. This matters for a TypeScript component the change introduces: its merge-base directory does not exist, and `surfaces()` already tolerates a base tree with no readable `package.json`, treating every head declaration as an addition. Without the `needsTree` guard, `prepare` would try to `npm install` the nonexistent base directory, fail, and turn every newly introduced TypeScript component into an `Unmeasurable` referral instead of a clean all-additions comparison. Regression test: `TestReviewPassesATypeScriptComponentThisChangeIntroduces` (`cmd/lydite/review_test.go:1138`).
