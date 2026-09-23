---
about: agentic/rules/ is the canonical, git-tracked source; .claude/rules/ and .agents/rules/ hold the same content but are gitignored render targets — a new rule file added to only one of the three is invisible to whichever tool reads a different one
saw:
  - .gitignore
  - agentic/rules/
---

`.gitignore` excludes `/.claude/` and `/.agents/` outright (lines naming both), so
`.claude/rules/*.md` and `.agents/rules/*.md` are never committed — only `agentic/rules/*.md` is.
Before this was noticed, `.claude/rules/` and `.agents/rules/` already held
`scope-a-concurrency-group-to-what-must-not-evict-it.md` that `agentic/rules/` was missing,
meaning the "canonical" source had already drifted behind its own rendered copies at least once.
A new rule is only durable if it lands in `agentic/rules/`; mirroring it into the other two by
hand (as done for consistency with existing practice) fixes the immediate read path but does
nothing to prevent the same drift recurring, since nothing currently re-derives `.claude/rules/`
and `.agents/rules/` from `agentic/rules/` automatically on this branch's evidence.
