---
name: annotation-body-silently-dropped-hash-comments
kind: gotcha
about: source/cli/internal/annotation/annotation.go
description: annotation.body only stripped a "//" introducer, so a Python "# [lydite:exclude_from_...]" comment passed through unchanged and never matched the marker prefix — the declaration was silently not a declaration, no error, no Unused entry, nothing in the output.
anchors:
  - path: source/cli/internal/annotation/annotation.go
    blob: 0ed0b9db58e59b974a14f31174a0ddf3f47dd882
confidence: verified
---

`body()` is the one function every declaration-reading call site (`Declarations`'s opening
comment, and `gather`'s continuation lines) routes through to strip a comment's introducer
before matching it against `annotation.Marker`. Before this branch it only handled `//`:
`strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(text, " \t"), "//"), " \t")`. A
Python `# [lydite:exclude_from_coverage][reason]` comment has no `//` to strip, so
`TrimPrefix` is a no-op and the leading `#` stays in front of the text — `CutPrefix(body,
marker)` never matches, and `Declarations` treats the comment as ordinary prose. Nothing
errors: the declaration is just never recognized, produces no `Unused` entry (since it was
never read as a declaration attempt at all), and silently deducts nothing from a coverage
or CRAP figure its author believed they'd excluded.

This existed from #214 (which gave Python a runner and lcov coverage measurement) until
this branch's fix — a `[lydite:exclude_from_coverage]` in a `.py` file was accepted by the
parser and did nothing, for the whole time Python had lcov coverage measurement but no
tree-sitter grammar to notice the mismatch. The fix is `body()` falling back to
`strings.TrimPrefix(text, "#")` when the `//` cut misses, so it now strips either
introducer. Worth checking for any other language-specific comment-stripping logic in this
codebase that might carry the same single-spelling assumption.
