---
name: rendered-rule-directories-are-gitignored-and-hand-mirroring-blocks-agtk-render
kind: gotcha
description: agentic/rules/ is the only tracked copy of the rules; .claude/rules/ and .agents/rules/ are gitignored render targets, and a rule hand-mirrored into one is an untracked file that makes agtk render refuse until --force.
anchors:
  - path: .gitignore
    blob: 84305dc62fca
  - path: .agentic-toolkit.yaml
    blob: 7b14f93d9bb8
confidence: verified
---

`.gitignore` excludes `/.claude/`, `/.codex/` and `/.agents/` (lines 34-39), so `.claude/rules/*.md` and `.agents/rules/*.md` are never committed — only `agentic/rules/*.md` is, and what a checkout's rendered directories say is local state. `.agentic-toolkit.yaml` renders for `platforms: [claude, codex]`. `agtk render` tracks the rule files it owns per platform in `.claude/.agtk-manifest.json` (keys `rules/<name>.md`) and `.agents/.agtk-manifest.json`; per `agtk render --help`, an existing file the manifest does not track makes the render refuse unless `--force` is passed.

So mirroring a new rule into `.claude/rules/` by hand, to keep one session's read path current, is exactly what stops every later render in that checkout: the refusal blocks all writes, and every rule added to `agentic/rules/` since never reaches either rendered directory. A hand copy is recognisable by lacking the `name:` front matter agtk's own rendered files carry, and it goes stale against its source silently. Observed once (2026-09-28): four hand-mirrored files sat untracked in both directories, `agentic/rules/` held 9 rules neither rendered directory had, and `agtk render --dry-run --force` planned the rewrite.

Before that was noticed, `.claude/rules/` and `.agents/rules/` already held a `scope-a-concurrency-group-to-what-must-not-evict-it.md` that `agentic/rules/` lacked, so the canonical source had drifted behind its own render targets. A new rule is durable only in `agentic/rules/`; run `agtk render --dry-run` to see what a render would refuse before mirroring anything by hand.
