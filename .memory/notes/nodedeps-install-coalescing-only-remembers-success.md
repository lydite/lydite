---
name: nodedeps-install-coalescing-only-remembers-success
kind: gotcha
description: nodedeps.Install marks a workspace root done only on success, so a failed install is retried by the next component resolving that root; the state is process-global.
anchors:
  - path: source/cli/internal/nodedeps/nodedeps.go
    blob: b3c37269f66d
confidence: suspect
---

`nodedeps.Install`'s per-root coalescing (a `sync.Map` mutex-per-root plus a `sync.Map` of completed roots, `installed.Load(root)` at `internal/nodedeps/nodedeps.go:349`, mirroring `internal/cargotool/cargotool.go`) records a root as done only on success. A failed install is never marked done, so the next component that resolves the same workspace root retries rather than inheriting the failure — a deliberate match to `cargotool`'s on-disk `Installed()` check (`fix/fresh-checkout-races`, lydite/lydite#186).

The state is process-global and keyed on the resolved root's cleaned path: scoped to one `lydite` process, so it says nothing about two invocations racing on the same workspace. The success-only recording was taken from the explorer's reading and not re-read line by line here, hence `suspect`.
