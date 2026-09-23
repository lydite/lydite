---
name: a-pythons-first-suite-comment-attaches-to-block-not-the-statement
kind: gotcha
about: source/cli/internal/treesitter/scope.go
description: Python's grammar puts the first comment of an indented suite as a sibling of the block node it precedes (never inside the block), so a declaration-scope walk that treats block as an ordinary anchor rather than stepping inside it silently widens to the whole suite when it holds more than one statement — masked when it holds only one, because the block's span then coincides with its sole child's.
anchors:
  - path: source/cli/internal/treesitter/scope.go
    blob: 98ffda9dda15033acea3a7f27827678db4102b19
confidence: verified
---

Verified directly by dumping the parse tree (`grammars.PythonLanguage()`) over
`class C:\n    # comment\n    def f(self): ...` and the same with a second method added.
In both cases the `comment` node is a child of `class_definition`, immediately preceding
`block` as a sibling — **never** a child of `block` itself, regardless of how many
statements the suite holds. `Grammar.scope`'s walk (`DeclaredExclusions` → `scope` →
`pastDecorations`) reaches `last.NextSibling()` from the comment and lands on `block`
directly.

If `block` is treated as an ordinary node — checked against `functions`/`decorations` and,
failing those, returned as the anchor — `introducesFunction`'s own recursive row-check
still finds the first statement inside `block` starting on the right row and returns
`true`, so the walk reports a match. But `span(anchor)` then reports `block`'s own span,
which is every statement in the suite, not just the first one the comment was written
above. With exactly one statement in the block this is invisible: `block`'s span and its
sole child's span are numerically identical, so the wrong node happens to produce the
right answer. With two or more statements the spans diverge and the bug surfaces: a
`[lydite:exclude_from_coverage]`/`[lydite:exclude_from_crap]` declaration above the first
of two class methods silently excludes both.

The fix treats `block` as a wrapper to step *inside* (its first child), exactly as
`decorated_definition` (Python's decorator wrapper) already needed to be — both live in
the same `decorated map[Grammar]map[string]bool` table, read by `pastDecorations` before
falling back to the ordinary decoration/anchor checks. This shipped as a real, silent bug
in the first Python-CRAP PR (#227) — its own test suite covered a decorated method
*outside* a class and a class's first (only) method, but nothing with a second statement
in the same suite, which is exactly the shape that exposes the coincidence.

Any future addition to `Grammar.scope`'s wrapper-stepping (a new grammar, a new Python
compound-statement shape) should check this exact case — a comment above the first of
*several* siblings inside the wrapper, not just above a lone one — before trusting a
passing test that only exercises the single-statement shape.
