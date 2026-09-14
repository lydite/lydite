---
name: pathmatch-seen-map-is-not-a-memo-cache
kind: invariant
description: pathmatch's `seen` map answers false for a revisited state, which is sound only because any success short-circuits the whole recursion.
anchors:
  - path: source/cli/internal/pathmatch/pathmatch.go
    blob: d9024d32dcfd
confidence: verified
---

`matchSegments` keys `seen` on `[2]int{len(pattern), len(target)}`, marks the state
before trying any `**` split point, and returns `false` outright if that key is seen
again (`pathmatch.go:57-61`). The comment above it (`pathmatch.go:45-49`) gives only
the performance motive — collapsing combinatorial `**` backtracking into a product of
the two lengths. It does not state why answering `false` is *correct*, and that is the
part worth re-deriving before touching this loop.

Two facts make it sound:

1. Lengths identify the subproblem. `pattern` only ever shrinks from the front
   (`pattern[1:]`, and `pattern, target = pattern[1:], target[1:]` at
   `pathmatch.go:82`), and `target` only by an absolute offset (`target[i:]`), so equal
   lengths really are the same subslices. That alone would justify caching the *true*
   answer, not always answering false.
2. Success short-circuits everything. The `**` loop is
   `if matchSegments(...) { return true }` (`pathmatch.go:65-68`), so the first `true`
   anywhere in the tree unwinds all of `Match` without exploring further. A state can
   therefore only be *revisited* after it has already been tried and failed — a state
   that succeeded is unreachable a second time.

Add a backtracking construct without property 2 (an alternation, or a `**` that can
match a partial segment) and this becomes a false negative: the pattern silently stops
matching things it should, with no panic and no infinite loop. `seen` is not an
ordinary memo table and must not be treated as one.
