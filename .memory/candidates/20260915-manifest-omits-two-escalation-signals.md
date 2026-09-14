---
about: the code-review manifest deliberately omits fix-revert and referencing_files from its escalation rules
saw:
  - .agentic-toolkit/code-review/manifest.yaml
---

`agtk code-review run`/`explain` treats a signal it cannot compute for the current diff as a
hard refusal, not a skip, whenever a manifest is present (a repo with no manifest at all gets
the built-in default's silent skip instead — `agtk code-review init`'s own help text states
this asymmetry directly). Two signals in the shipped built-in default manifest hit this on an
ordinary multi-commit branch:

- `fix-revert` (git-blame search for a revert/security-repair pattern) gives up and refuses
  once a diff's hunk count passes an internal cap (observed: 25). A branch of five-plus
  commits touching a dozen-plus files reaches that easily.
- `referencing_files` (symbol-reference count) refuses outright on any diff containing a file
  type its symbol extractor doesn't cover — confirmed failing on a diff mixing Go with
  Markdown and lcov (`.info`) fixture files, with no way to exclude just those files from the
  signal's computation.

Both errors name the fix: remove the signal from the escalation rule's `signals: {in: [...]}`
list (or, for `referencing_files`, delete the whole rule, since it has no sibling signal to
fall back to), or narrow with `touches:`. Neither is scoped by `--panel` — naming a panel
explicitly does not skip evaluating the escalation rules, so a broken signal blocks review of
every target and every panel choice until the manifest itself is edited.

This repo's manifest (adopted via `agtk code-review init --force` from the current built-in
default, then edited) has both rules removed rather than narrowed, because no `touches:` path
pattern was obviously the right scope and removing the specific signal preserved the rest of
each rule's other conditions (`auth`, `crypto`, `concurrency`, `sensitive-data` still escalate
normally).
