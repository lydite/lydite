---
about: source/cli/internal/nodedeps/nodedeps.go
saw: source/cli/internal/nodedeps/nodedeps.go:Install, installLocks, installed
---

`nodedeps.Install`'s per-root coalescing (a `sync.Map` mutex-per-root plus a `sync.Map` of
completed roots, mirroring `internal/cargotool/cargotool.go:111-126`) records a root as done
only on success. A failed install is never marked done, so the next component that resolves the
same workspace root retries the install rather than inheriting the earlier failure. This
mirrors `cargotool`'s own on-disk `Installed()` check, which likewise leaves a failed install
retryable, and was a deliberate match rather than an independent choice
(`fix/fresh-checkout-races`, task 2 of `lydite/lydite#186`).

The coalescing state is process-global and keyed on the resolved root's cleaned path — scoped
to one `lydite` process, not across processes — so it says nothing about two `lydite` invocations
racing on the same workspace.
