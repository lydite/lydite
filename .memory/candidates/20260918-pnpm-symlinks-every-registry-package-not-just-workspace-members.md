---
about: pnpm's default node_modules layout symlinks every installed registry package, not only a workspace member
saw:
  - source/cli/internal/typescript/licence.go
  - agentic/rules/a-symlink-in-node-modules-is-not-necessarily-a-workspace-member.md
---

Under pnpm's default layout, `node_modules/<pkg>` is a symlink into pnpm's own content-addressed
`.pnpm` store (`node_modules/.pnpm/<pkg>@<version>/node_modules/<pkg>`) for *every* installed
package, ordinary registry dependencies included — not only a workspace member the way yarn and
npm link one. Code that treats "this node_modules entry is a symlink" as synonymous with "this is
the repository's own local package" is wrong for pnpm specifically: it will skip almost every
real dependency.

This shipped as a real bug in `internal/typescript/licence.go`'s first version (caught by a
`panel-code-review` `deep` pass, RED, corroborated by two independent reviewer runs, fixed in
commit `90bf4e2` on `feat/licence-typescript`) — `installedPackage` excluded every symlink
outright, which under pnpm reads almost nothing and passes a component nothing was measured
against (a fail-open ADR 0038 explicitly forbids: "a gate that could not run must not render as
one that ran and found nothing"). The fix resolves where the symlink actually points
(`filepath.EvalSymlinks`) and compares against the resolved `node_modules` root: resolving inside
`node_modules` (into `.pnpm`) is an installed package to read; resolving outside it and back into
the repository is the workspace member to skip. See the rule file for the prescriptive form and
`docs/adr/0042` for the design this protects.

Also noted while fixing this: comparing an *unresolved* `node_modules` root against a *resolved*
symlink target is its own bug on macOS, because `t.TempDir()` (and some real filesystems) reach
their path through a symlinked ancestor (`/var` → `/private/var` on darwin) — resolve the root
once, up front, before comparing anything against it.
