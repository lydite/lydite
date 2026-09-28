---
name: serena-project-yml-drifts-unrelated-to-branch-work
kind: gotcha
description: .serena/project.yml shows as modified in `git status` independent of any branch work — a background Serena language-server-list regeneration, not something a session's own commits touched.
anchors:
  - path: .serena/project.yml
    blob: 68dfcfb012d8
confidence: suspect
---

Across one working session, `.serena/project.yml` reappeared as modified in `git status` twice, in
between commits that never touched it — each time with the same shape of diff (the commented
`language_servers` id list gaining/losing entries, and a couple of comment lines under "Special
requirements"). Neither modification was made by any task in that session's own work. Look at `git
diff .serena/project.yml` before attributing a change there to anything a session did: this shape —
comment/list churn unrelated to the task at hand — is not a change any of this repository's
branches should carry.
</content>
