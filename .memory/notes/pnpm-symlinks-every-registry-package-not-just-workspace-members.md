---
name: pnpm-symlinks-every-registry-package-not-just-workspace-members
kind: gotcha
description: Under pnpm every node_modules/<pkg> is a symlink into the .pnpm store, so treating a symlink as a workspace member skips almost every real dependency; resolve the target instead.
anchors:
  - path: source/cli/internal/typescript/licence.go
    blob: c1e23b1d206f
confidence: verified
---

Under pnpm's default layout `node_modules/<pkg>` is a symlink into `node_modules/.pnpm/<pkg>@<version>/node_modules/<pkg>` for *every* installed package, registry dependencies included — unlike yarn and npm, which link only a workspace member. Code treating "is a symlink" as "is the repository's own package" skips nearly every real dependency.

This shipped as a bug in the first `internal/typescript/licence.go`: `installedPackage` excluded every symlink outright, so under pnpm it read almost nothing and passed a component nothing was measured against — a fail-open ADR 0038 forbids. The fix resolves where the symlink points with `filepath.EvalSymlinks` (`licence.go:409`) and compares against the *resolved* `node_modules` root (`licence.go:352`): resolving inside `node_modules` (into `.pnpm`) is an installed package to read; resolving outside it and back into the repository is a workspace member to skip. Resolve the root once, up front — comparing an unresolved root against a resolved target breaks wherever a temp dir is reached through a symlinked ancestor (`/var` → `/private/var` on darwin). See ADR 0042.
