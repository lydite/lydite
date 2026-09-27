---
about: whether a Go type can be moved out of cmd/lydite into a new internal package, when other unowned cmd/lydite files reach into it, is decided by whether those files reach a field or method (blocks the move) versus merely call a function that takes or returns the type (safe to move behind a same-signature wrapper)
saw:
  - source/cli/cmd/lydite/measurements.go
  - source/cli/cmd/lydite/mutation.go
  - source/cli/cmd/lydite/merge.go
  - source/cli/cmd/lydite/record.go
  - source/cli/agentic/references/architecture.md
targets: null
verdict: null
---

Established while migrating `lydite test` onto Flow (#270), where a first draft assumed "move a
type, leave a same-name shim" covers every caller a function-level shim covers. It does not: an
unexported field or method on a type is unreachable the moment the type changes package, and no
alias or rename survives that for a caller in a different package the migrating session cannot
edit.

`componentPlan`, `componentLog`, `measurementsDoc` and `componentMeasurement` all stayed in
`cmd/lydite` for exactly this reason — `mutation.go` reaches `p.c`/`p.log`/`.ready`/`.row`/
`.ports`, `merge.go` calls `.asMeasurement(c)`/`.patchPartOf(...)`, `record.go` calls
`.snapshot()` — while `measurement`/`patchPart` (also coverage/CRAP types) became real type
aliases over `internal/test/measure`'s exported structs, because task 1's analysis confirmed no
unowned file called an unexported method on either, only exported fields and the type name
itself.

The general check for a future extraction: grep every candidate caller for the type's own
unexported field selectors and method calls, not just its name. A name appearing only in a
function call's argument or return position is safe to move behind a same-signature wrapper; a
name appearing as `x.unexportedThing` or `x.unexportedMethod()` means the type itself has to
stay where the caller can see it.
