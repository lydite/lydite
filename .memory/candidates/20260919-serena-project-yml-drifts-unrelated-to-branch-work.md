---
name: serena-project-yml-drifts-unrelated-to-branch-work
kind: gotcha
about: .serena/project.yml
description: .serena/project.yml shows as modified in `git status` independent of any branch work — a background Serena language-server-list regeneration, not something a session's own commits touched.
anchors:
  - path: .serena/project.yml
    blob: 726f56065b5cd340e496d5df3574094d914ef628
confidence: verified
---

Across one working session, `.serena/project.yml` reappeared as modified in `git status`
twice, in between commits that never touched it — each time with the same shape of diff
(the commented `language_servers` id list gaining/losing entries like `julia_fatou`, and
a couple of comment lines under "Special requirements"). Neither modification was made by
any task in that session's own work.

Before committing anything from a session that touches this repository, check `git diff
.serena/project.yml`: if the diff is only this kind of comment/list churn with no relation
to the task at hand, it is safe to `git checkout -- .serena/project.yml` and continue — it
is not a change any of this repository's branches should carry.
