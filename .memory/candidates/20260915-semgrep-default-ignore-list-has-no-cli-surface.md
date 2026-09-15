---
about: Semgrep publishes no command to print its built-in default .semgrepignore patterns — the list has to be extracted from the compiled semgrep-core binary
saw:
  - source/cli/internal/semgrep/semgrep.go
  - source/cli/internal/semgrep/requirements.txt
---

`semgrep show --help` (checked against the pinned version) has no subcommand that dumps the
default ignore list Semgrep applies when a scan root carries no `.semgrepignore` of its own.
Nothing under the pip-installed `semgrep` Python package ships it as a readable resource file
either — searching the installed package tree for `.semgrepignore`/`ignore` turns up nothing.

The list exists as a literal template string embedded inside the compiled `semgrep-core`
binary the `semgrep` pip package ships (found under `.../site-packages/semgrep/bin/semgrep-core`
inside the pipx venv). Running `strings -n 3` on that binary and searching for the marker string
`"default semgrepignore patterns"` finds the block immediately after it — that block is exactly
the default `.semgrepignore` template (comments and all): `.git`, `.svn`, `.hg`, `_darcs`, `CVS`,
`build/`, `vendor/`, `dist/`, `*.min.js`, `.env/`, `.tox/`, `node_modules/`, `.npm/`, `.yarn/`,
`.venv/`, `_opam/`, `_build/`, `_cargo/`, `test/`, `tests/`, `testsuite/`, `*_test.go` (as of the
1.176.x line).

`internal/semgrep.go`'s `defaultIgnorePatterns` vendors this list, and its doc comment names this
exact extraction method as the way to re-verify it whenever `requirements.txt`'s pinned Semgrep
version bumps — there is no faster or more authoritative way to get the list than re-running
`strings` against the newly-pinned version's binary.
