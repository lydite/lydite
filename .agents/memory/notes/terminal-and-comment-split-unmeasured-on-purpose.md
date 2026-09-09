---
name: terminal-and-comment-split-unmeasured-on-purpose
kind: rationale
description: The terminal groups refer/unmeasured/dropped into one amber class while the PR comment deliberately splits unmeasured out — do not unify the two status maps.
anchors:
  - path: source/cli/internal/ui/row.go
    blob: c2b0747f975b
  - path: source/cli/internal/ui/color.go
    blob: b0720bfd4a7d
  - path: source/cli/internal/ui/comment.go
    blob: 9574cbfffc19
confidence: verified
---

Two renderers of the same `Status` set make opposite, deliberate choices about
`StatusUnmeasured`, and neither file's doc comment mentions the other.

- Terminal: `glyph()` cases `StatusRefer, StatusUnmeasured, StatusDropped` together
  (`ui/row.go:58`) and `statusRGB` gives all three the same amber `0xF0B429`
  (`ui/color.go:26-28`). `row.go:10` states the reason: several statuses want the same
  amount of attention while voting differently.
- PR comment: `mark()` returns a yellow circle for `StatusRefer` and a white one for
  `StatusUnmeasured` (`ui/comment.go:179-182`) — different glyph and different implied
  colour. Its own doc comment argues the opposite: "nobody looked" is a different thing
  from "somebody must", and rendering them alike is what lets a gate that never ran hide
  among the ones asking for attention.

Both are internally consistent and each cites a real reason, so this is not a bug — but
it means the two maps must not be DRY'd into one shared glyph/colour table. Collapsing
toward the terminal's grouping reintroduces exactly the failure the comment renderer
warns about: a gate that never ran, hiding among the ones asking for attention. Check
`docs/design/tokens.md` before assuming either mapping is the canonical one; they differ
by design surface.
