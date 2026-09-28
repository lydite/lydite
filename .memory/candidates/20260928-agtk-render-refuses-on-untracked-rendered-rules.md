---
about: agtk render refuses to run in a checkout whose .claude/rules or .agents/rules holds a rule file the platform's .agtk-manifest.json does not track — which hand-mirroring a rule there produces — and until it runs, rules added to agentic/rules/ never reach either rendered directory; a --force render rewrites every stale or missing managed file at once
saw:
  - .agentic-toolkit.yaml
  - .gitignore
  - .claude/.agtk-manifest.json
  - .agents/.agtk-manifest.json
  - .claude/rules/guard-an-in-process-comparison-by-whether-a-credential-exists-not-by-a-flag.md
  - .claude/rules/never-name-an-artifact-inside-publishs-lydite-reports-glob.md
  - agentic/rules/guard-an-in-process-comparison-by-whether-a-credential-exists-not-by-a-flag.md
  - agentic/rules/a-row-a-shared-helper-already-decided-crosses-into-a-stage-as-an-opaque-error.md
---

`.agentic-toolkit.yaml` renders for `platforms: [claude, codex]`. Every render output —
`/.claude/`, `/.agents/`, `/.codex/`, `/CLAUDE.md`, `/AGENTS.md` — is gitignored (`.gitignore`
lines ~34-39), so what each checkout's rendered rules say is local state, and only
`agentic/rules/` is shared. `agtk render` tracks the rule files it owns per platform in
`.claude/.agtk-manifest.json` (keys `rules/<name>.md`) and `.agents/.agtk-manifest.json` (keys
`.agents/rules/<name>.md`); per `agtk render --help`, an existing file not in the manifest causes
a refusal unless `--force` is passed.

State of this worktree on 2026-09-28 (`agtk render --dry-run`, which writes nothing): the render
refuses on four files — `guard-an-in-process-comparison-by-whether-a-credential-exists-not-by-a-flag.md`
and `never-name-an-artifact-inside-publishs-lydite-reports-glob.md`, each in both `.claude/rules/`
and `.agents/rules/`, present on disk but in neither manifest (both manifests track 26 rules;
each directory holds 28). The first is also stale against its source: the rendered copy still
names `computeAPISurfaces` and `runClearance` where `agentic/rules/` now names
`reviewdecision.Surfaces` and `scmstages.InitSCM`/`ErrNoCredential`. The rendered
`never-name-an-artifact...` copy matches its source byte for byte but lacks the `name:` front
matter agtk's own rendered files carry — the mark of a hand copy.

Because the refusal blocks every write, `agentic/rules/` (37 rules) has 9 that neither rendered
directory holds — among them `a-row-a-shared-helper-already-decided-crosses-into-a-stage-as-an-opaque-error.md`,
`a-stage-is-a-function-of-its-own-in-and-imports-nothing-above-it.md` and
`write-a-stages-diagnostic-as-it-arises-not-returned-in-out.md` — so a session reading
`.claude/rules/` does not see them. `agtk render --dry-run --force` plans 36 writes: 16 files
under `.claude/rules/` and 16 under `.agents/rules/` (9 new, 7 updated in each), plus the managed
region of `CLAUDE.md`, the managed keys of `.claude/settings.json`, `AGENTS.md`, and the managed
keys of `.codex/config.toml`.

This is the drift `20260923-three-rule-directories-are-rendered-and-two-are-gitignored.md`
predicted: a rule mirrored by hand into `.claude/rules/`/`.agents/rules/` is exactly an existing
file the manifest does not track, so the mirroring that keeps one session's read path current is
what stops every later render in that checkout.
