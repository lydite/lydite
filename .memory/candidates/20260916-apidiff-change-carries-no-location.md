---
about: golang.org/x/exp/apidiff's Change type carries no package path and no token.Pos, so locating an incompatible change at a source line is entirely the caller's own work
saw:
  - source/cli/internal/apisurface/apisurface.go
---

`apidiff.Change` is `{Message string; Compatible bool}` and nothing else — no structured symbol
reference, no position. `apidiff.Changes`/`ModuleChanges`'s only output is that flat message
string, formatted as `<name>: <what happened>` (`Removed: removed`, `Store.Put: added`,
`Config.Timeout: changed from int to int64`).

`internal/apisurface`'s `symbol()` (`apisurface.go`) recovers the name with
`strings.Cut(message, ": ")` — safe because `messageSet.collect` (in the `apidiff` package
itself, unexported) always formats it that way. `pkg.resolve()` then walks the name against the
loaded `*types.Package`: the first segment via `Scope().Lookup` (trimming a `(*T)` receiver's
punctuation), each later dotted segment via `types.LookupFieldOrMethod` — which handles both
struct fields and interface methods, so `Store.Put` resolves to the *method's* declaration line,
not the interface's.

There is no lower-level per-object API to use instead: `apidiff` exports only `Changes`,
`ModuleChanges`, `Report`, `Change`, `Module`. Anyone reaching for structured location data from
this library needs to parse the message, same as this package does.
