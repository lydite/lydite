---
about: source/cli/internal/runner/runner.go
saw: goScope function and its call site in Runner.Producer
---

`goScope` in `source/cli/internal/runner/runner.go` returns `""` for a Go component's default
scope — no declared `args`, `./...`, or flags with a trailing `./...` pattern — so `Producer`
falls back to `join("go", lang)`, byte-identical to what it returned before scope-folding
existed. Only a component whose `args:` declare a non-default `-coverpkg` or a package pattern
other than `./...` gets the new `, scope ...` suffix and therefore a one-time `new`/`not
compared` baseline reset.

This matters because a plan or handoff describing this change may predict a "fleet-wide" reset
of every Go component's producer string. That prediction does not hold against the actual
implementation: a repository whose components all declare the default scope (or no `args` at
all) sees no producer change and no baseline reset. Verify against `goScope`'s early return
before repeating a fleet-wide-reset claim.
