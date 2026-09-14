---
about: the anchor .agents/references/configuration.md has moved to agentic/references/configuration.md
saw:
  - agentic/references/configuration.md
targets: no-lydite-annotation-excludes-a-scanner-finding
verdict: still-true
---

The note's claim is unaffected — `.lydite/config.yml` "does one thing" language and the
gosec/semgrep/clippy-suppression stance are still current — but the anchor path itself
moved. `.agents/references/configuration.md` no longer exists; the file is now at
`agentic/references/configuration.md` (confirmed with `ls`). This is the toolkit
source-tree move in commit b3d23a2 ("docs(agentic): move the toolkit source tree to
agentic/ and slim the map"). The line range cited (`:11-12`) was not re-verified against
the new path's content in this task, only the path's existence.
