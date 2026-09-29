---
name: go-producer-scope-default-is-empty-not-fleet-wide
kind: gotcha
description: goScope returns empty for a default scope, so a Go producer string changes, and a baseline resets, only for a component declaring a non-default -coverpkg or package pattern, never fleet-wide.
anchors:
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
confidence: verified
---

`goScope` (`internal/runner/runner.go:1490`) returns `""` for a Go component's default scope — no declared `args`, `./...`, or flags with a trailing `./...` pattern — so `Producer` falls back to `join("go", lang)`, byte-identical to what it returned before scope-folding existed. Only a component declaring a non-default `-coverpkg` or a package pattern other than `./...` gets the `, scope ...` suffix and therefore a one-time `new`/`not compared` baseline reset.

A plan or handoff predicting a "fleet-wide" reset of every Go component's producer string is wrong: a repository whose components all declare the default scope (or no `args`) sees no producer change and no reset. Check `goScope`'s early return before repeating that claim.
