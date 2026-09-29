---
name: workspaceroot-fails-on-an-ambiguous-lockfile-root
kind: gotcha
description: nodedeps.WorkspaceRoot returns not-ok for a directory holding more than one recognised lockfile, so the typescript.install override cannot be made to require a resolved root.
anchors:
  - path: source/cli/internal/nodedeps/nodedeps.go
    blob: b3c37269f66d
confidence: verified
---

`nodedeps.WorkspaceRoot` (`internal/nodedeps/nodedeps.go:94-111`) walks up from a component's dir to the nearest ancestor holding any recognised lockfile, then calls `Manager(d)` there (`:102`) and returns `("", false)` if that is not-ok — i.e. more than one recognised lockfile is present. So `WorkspaceRoot` itself already fails on the ambiguous-root case, not only `Manager` in isolation.

That matters for reasoning about the `typescript.install` override (ADR 0055): "always require `WorkspaceRoot` to resolve before running an override" would make the override unusable for exactly the case its doc comment (`internal/config/config.go`, `TypeScriptLanguage.Install`) says it exists for — an ambiguous multi-lockfile root has no single root to resolve, by construction.
