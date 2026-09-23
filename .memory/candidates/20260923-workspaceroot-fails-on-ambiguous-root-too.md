about: WorkspaceRoot refuses to resolve on an ambiguous multi-lockfile root, not only Manager
saw:
  - source/cli/internal/nodedeps/nodedeps.go
---

`nodedeps.WorkspaceRoot` (nodedeps.go:92-109) walks up from a component's dir to the
nearest ancestor holding any recognised lockfile, then calls `Manager(d)` on that
directory (nodedeps.go:100) and returns `("", false)` if that's not-ok — i.e. more than
one recognised lockfile present there. This means `WorkspaceRoot` itself already fails
on the ambiguous-root case, not just `Manager` in isolation: a caller cannot get a root
back for an ambiguous directory even before asking what package manager to use there.

This matters for anything reasoning about `typescript.install`'s override behavior
(nodedeps.go:233-240): "always require WorkspaceRoot to resolve before running an
override" would make the override unusable for exactly the case its own doc comment
(internal/config/config.go, `TypeScriptLanguage.Install`) says it exists for — the
ambiguous multi-lockfile root has no single root to resolve, by construction.
Established while drafting docs/adr/0055-an-install-step-is-a-setup-command-and-the-override-still-coalesces.md.
